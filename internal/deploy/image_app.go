package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// DeployImageApp deploys (or re-deploys) a "image"-kind app's container/pod
// directly into its stack's namespace/network, aliased as the app's name —
// the Phase-4 counterpart to ProcessPush for "git" kind apps. There is no
// clone/build/compliance pipeline: imageRef and port are taken as-is from
// deployment_apps, and any user-supplied environment variables are decrypted
// from image_env_secret_id (e.g. POSTGRES_PASSWORD for a postgres:16 db app).
// Called from handleAddApp on creation and handleTriggerApp for re-deploys.
func (o *Orchestrator) DeployImageApp(ctx context.Context, appID string) error {
	var (
		stackName     string
		appName       string
		imageRef      string
		port          int32
		targetKind    string
		envSecretID       *string
		volumePath        string
		secretFilesID     *string
		namespaceOverride string
	)
	err := o.pool.QueryRow(ctx, `
		SELECT d.name, da.name, COALESCE(da.image_ref, ''), COALESCE(da.port, 0), da.target_kind, da.image_env_secret_id::text, COALESCE(da.volume_path, ''), da.secret_files_id::text, COALESCE(d.namespace_override, '')
		FROM deployment_apps da
		JOIN deployments d ON d.id = da.deployment_id
		WHERE da.id = $1
	`, appID).Scan(&stackName, &appName, &imageRef, &port, &targetKind, &envSecretID, &volumePath, &secretFilesID, &namespaceOverride)
	if err != nil {
		return fmt.Errorf("deploy: look up image app %s: %w", appID, err)
	}

	k8sNamespace := namespaceName(stackName)
	if namespaceOverride != "" {
		k8sNamespace = k8sResourceName(namespaceOverride)
	}

	env := map[string]string{}
	if envSecretID != nil {
		plaintext, err := o.vault.GetByID(ctx, *envSecretID)
		if err != nil {
			return fmt.Errorf("deploy: decrypt image env for %s: %w", appID, err)
		}
		if err := json.Unmarshal(plaintext, &env); err != nil {
			return fmt.Errorf("deploy: decode image env for %s: %w", appID, err)
		}
	}

	secretFiles, err := o.loadSecretFiles(ctx, secretFilesID)
	if err != nil {
		return fmt.Errorf("deploy: load secret files for %s: %w", appID, err)
	}

	var deployErr error
	switch {
	case targetKind == "local_k8s" && o.kubeDeployer != nil:
		deployErr = o.kubeDeployer.KubeDeployImageApp(ctx, appName, k8sNamespace, imageRef, port, env, volumePath, secretFiles)
	case targetKind == "local_docker" && o.dockerDeployer != nil:
		deployErr = o.dockerDeployer.DockerDeployImageApp(ctx, stackName, appName, imageRef, uint16(port), env, volumePath, secretFiles)
	default:
		return fmt.Errorf("deploy: no deployer available for target_kind %q", targetKind)
	}
	if deployErr != nil {
		dep, _ := o.persistStatus(ctx, appID, "failed")
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		return fmt.Errorf("deploy: image deploy %s: %w", appName, deployErr)
	}

	var dep Deployment
	if targetKind == "local_k8s" {
		dep, err = o.persistRunning(ctx, appID, k8sNamespace)
	} else {
		dep, err = o.persistStatus(ctx, appID, "running")
	}
	if err != nil {
		return err
	}
	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
	return nil
}
