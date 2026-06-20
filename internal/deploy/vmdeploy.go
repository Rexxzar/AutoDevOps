package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Rexxzar/autodevops/internal/sshutil"
)

// vmAppPort is the container port Paketo-built images listen on (the PORT
// env var contract honored by internal/docker.DockerDeploy) — the cloud_vm
// deployer publishes the same port on the VM at the deterministic host port
// dockerAppHostPort would assign locally.
const vmAppPort = 8080

// loadCloudVMResource reads the provisioned_resources row identified by
// resourceID — a deployment_apps.target_resource_id for a cloud_vm app —
// and validates it is an applied "vm" resource before the SSH deployer
// dials it.
func (o *Orchestrator) loadCloudVMResource(ctx context.Context, resourceID string) (cloudVMResource, error) {
	var res cloudVMResource
	var resourceType, status, outputsJSON string
	err := o.pool.QueryRow(ctx, `
		SELECT name, provider, resource_type, COALESCE(cloud_account_id::text, ''), region, status, COALESCE(outputs::text, '{}')
		FROM provisioned_resources WHERE id = $1
	`, resourceID).Scan(&res.Name, &res.Provider, &resourceType, &res.CloudAccountID, &res.Region, &status, &outputsJSON)
	if err != nil {
		return cloudVMResource{}, fmt.Errorf("deploy: look up target resource %s: %w", resourceID, err)
	}
	if resourceType != "vm" {
		return cloudVMResource{}, fmt.Errorf("deploy: target resource %s is not a vm (resource_type=%s)", resourceID, resourceType)
	}
	if status != "applied" {
		return cloudVMResource{}, fmt.Errorf("deploy: target resource %s is not applied (status=%s)", resourceID, status)
	}
	if err := json.Unmarshal([]byte(outputsJSON), &res.Outputs); err != nil {
		return cloudVMResource{}, fmt.Errorf("deploy: decode outputs for resource %s: %w", resourceID, err)
	}
	return res, nil
}

// deployToVM ships pushRef — an image buildImage already built into the local
// Docker daemon — to res over SSH: dial once, stream a "docker save" tar into
// "docker load", then (re)create the app's container with vmAppPort published
// on the same deterministic 8100-8999 host port DockerDeploy would bind
// locally (dockerAppHostPort), with extraEnv (plus PORT) injected.
func (o *Orchestrator) deployToVM(ctx context.Context, stackName, appName, pushRef string, extraEnv map[string]string, res cloudVMResource) error {
	if o.imageSaver == nil {
		return fmt.Errorf("deploy: no image saver configured (Docker daemon unavailable)")
	}

	client, err := o.dialCloudVM(ctx, res)
	if err != nil {
		return err
	}
	defer client.Close()

	cname := sanitizeDockerName(stackName) + "-" + sanitizeDockerName(appName)
	localTag := cname + ":latest"

	tarStream, err := saveImageFromRegistry(pushRef, localTag)
	if err != nil {
		return fmt.Errorf("deploy: save image %s: %w", pushRef, err)
	}
	defer tarStream.Close()

	if result, err := sshutil.ExecOnClient(client, "sudo docker load", tarStream); err != nil {
		return fmt.Errorf("deploy: docker load on %s: %w", res.Name, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("deploy: docker load on %s failed (exit %d): %s", res.Name, result.ExitCode, result.Stderr)
	}

	hostPort := dockerAppHostPort(stackName, appName)

	env := make(map[string]string, len(extraEnv)+1)
	for k, v := range extraEnv {
		env[k] = v
	}
	env["PORT"] = fmt.Sprintf("%d", vmAppPort)

	runCmd := fmt.Sprintf(
		"sudo docker rm -f %s >/dev/null 2>&1; sudo docker run -d --name %s --restart unless-stopped -p %d:%d %s %s",
		shellQuote(cname), shellQuote(cname), hostPort, vmAppPort, envFlags(env), shellQuote(localTag),
	)

	result, err := sshutil.ExecOnClient(client, runCmd, nil)
	if err != nil {
		return fmt.Errorf("deploy: docker run on %s: %w", res.Name, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("deploy: docker run on %s failed (exit %d): %s", res.Name, result.ExitCode, result.Stderr)
	}
	return nil
}

// envFlags renders env as a sorted, shell-quoted sequence of "-e KEY=VALUE"
// flags for "docker run" — sorted for deterministic command strings across
// redeploys, mirroring internal/docker's envSlice ordering rationale.
func envFlags(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	flags := make([]string, 0, len(keys))
	for _, k := range keys {
		flags = append(flags, "-e "+shellQuote(fmt.Sprintf("%s=%s", k, env[k])))
	}
	return strings.Join(flags, " ")
}

// shellQuote wraps s in single quotes for safe inclusion in a remote shell
// command, escaping embedded single quotes — duplicated from
// internal/console/aci.go per the internal/ package-boundary rule.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
