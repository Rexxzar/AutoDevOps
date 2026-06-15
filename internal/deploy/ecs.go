package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsResource is the subset of a provisioned_resources row needed to redeploy
// a git app onto an ECS Fargate service — duplicated from
// console.Resource/cloudVMResource per the internal/ package-boundary rule
// (internal/deploy must not import internal/console).
type ecsResource struct {
	Name           string
	CloudAccountID string
	Region         string
	Outputs        map[string]any
}

// loadECSResource reads the provisioned_resources row identified by
// resourceID — a deployment_apps.target_resource_id for a cloud_ecs app —
// and validates it is an applied "ecs" resource before deployToECS runs.
func (o *Orchestrator) loadECSResource(ctx context.Context, resourceID string) (ecsResource, error) {
	var res ecsResource
	var resourceType, status, outputsJSON string
	err := o.pool.QueryRow(ctx, `
		SELECT name, resource_type, COALESCE(cloud_account_id::text, ''), region, status, COALESCE(outputs::text, '{}')
		FROM provisioned_resources WHERE id = $1
	`, resourceID).Scan(&res.Name, &resourceType, &res.CloudAccountID, &res.Region, &status, &outputsJSON)
	if err != nil {
		return ecsResource{}, fmt.Errorf("deploy: look up target resource %s: %w", resourceID, err)
	}
	if resourceType != "ecs" {
		return ecsResource{}, fmt.Errorf("deploy: target resource %s is not an ecs service (resource_type=%s)", resourceID, resourceType)
	}
	if status != "applied" {
		return ecsResource{}, fmt.Errorf("deploy: target resource %s is not applied (status=%s)", resourceID, status)
	}
	if err := json.Unmarshal([]byte(outputsJSON), &res.Outputs); err != nil {
		return ecsResource{}, fmt.Errorf("deploy: decode outputs for resource %s: %w", resourceID, err)
	}
	return res, nil
}

// deployToECS ships pushRef — an image buildImage already built into the
// local Docker daemon — to res: retag and push it to the deployment's bundled
// ECR repository, register a new task-definition revision pointing at the new
// image (with extraEnv plus PORT applied), and force a new deployment of the
// ECS service onto that revision.
func (o *Orchestrator) deployToECS(ctx context.Context, pushRef string, extraEnv map[string]string, res ecsResource) error {
	if o.pusher == nil {
		return fmt.Errorf("deploy: no image pusher configured (Docker daemon unavailable)")
	}

	repoURL := outputString(res.Outputs, "ecr_repository_url")
	clusterName := outputString(res.Outputs, "ecs_cluster_name")
	if repoURL == "" || clusterName == "" {
		return fmt.Errorf("deploy: resource %s is missing ecr_repository_url/ecs_cluster_name outputs — apply it with a bundled ECR repository first", res.Name)
	}

	auth, err := o.ecrAuth(ctx, res.CloudAccountID, res.Region, repoURL)
	if err != nil {
		return err
	}

	ecrRef := repoURL + ":" + imageTag(pushRef)
	if err := o.pusher.TagImage(ctx, pushRef, ecrRef); err != nil {
		return err
	}
	if err := o.pusher.PushImage(ctx, ecrRef, &auth); err != nil {
		return err
	}

	cfg, err := o.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return err
	}
	client := ecs.NewFromConfig(cfg)

	descOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(res.Name),
	})
	if err != nil {
		return fmt.Errorf("deploy: ecs describe task definition %s: %w", res.Name, err)
	}
	td := descOut.TaskDefinition
	if len(td.ContainerDefinitions) == 0 {
		return fmt.Errorf("deploy: ecs task definition %s has no container definitions", res.Name)
	}

	containerDef := td.ContainerDefinitions[0]
	containerDef.Image = aws.String(ecrRef)

	port := vmAppPort
	if len(containerDef.PortMappings) > 0 && containerDef.PortMappings[0].ContainerPort != nil {
		port = int(*containerDef.PortMappings[0].ContainerPort)
	}
	env := make(map[string]string, len(extraEnv)+1)
	for k, v := range extraEnv {
		env[k] = v
	}
	env["PORT"] = strconv.Itoa(port)
	containerDef.Environment = environmentKeyValuePairs(env)

	regOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  td.Family,
		ContainerDefinitions:    []types.ContainerDefinition{containerDef},
		Cpu:                     td.Cpu,
		Memory:                  td.Memory,
		NetworkMode:             td.NetworkMode,
		RequiresCompatibilities: td.RequiresCompatibilities,
		ExecutionRoleArn:        td.ExecutionRoleArn,
		TaskRoleArn:             td.TaskRoleArn,
		RuntimePlatform:         td.RuntimePlatform,
	})
	if err != nil {
		return fmt.Errorf("deploy: ecs register task definition %s: %w", res.Name, err)
	}

	if _, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:            aws.String(clusterName),
		Service:            aws.String(res.Name),
		TaskDefinition:     regOut.TaskDefinition.TaskDefinitionArn,
		ForceNewDeployment: true,
	}); err != nil {
		return fmt.Errorf("deploy: ecs update service %s: %w", res.Name, err)
	}
	return nil
}

// imageTag returns the tag portion of an image reference of the form
// "host:port/name:tag" — used to derive the ECR/ACR tag from buildImage's
// local-registry pushRef so the same git-commit SHA tags the image in both
// the local registry and the cloud registry.
func imageTag(ref string) string {
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		return ref[idx+1:]
	}
	return "latest"
}

// environmentKeyValuePairs renders env as a sorted slice of
// ecs/types.KeyValuePair — sorted for deterministic task-definition diffs
// across redeploys, mirroring envFlags' ordering rationale for cloud_vm.
func environmentKeyValuePairs(env map[string]string) []types.KeyValuePair {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]types.KeyValuePair, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, types.KeyValuePair{Name: aws.String(k), Value: aws.String(env[k])})
	}
	return pairs
}
