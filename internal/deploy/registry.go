package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/docker/docker/api/types/registry"
)

// loadRegistryOutputs reads the outputs of an applied "ecr"/"acr"
// provisioned_resources row identified by registryResourceID — used by
// loadECSResource/loadACIResource to resolve registry connection info from a
// linked standalone registry resource (see migration
// 020_registry_resource_link.sql) rather than the ECS/ACI resource's own
// outputs.
func (o *Orchestrator) loadRegistryOutputs(ctx context.Context, registryResourceID, wantType string) (map[string]any, error) {
	var resourceType, status, outputsJSON string
	err := o.pool.QueryRow(ctx, `
		SELECT resource_type, status, COALESCE(outputs::text, '{}')
		FROM provisioned_resources WHERE id = $1
	`, registryResourceID).Scan(&resourceType, &status, &outputsJSON)
	if err != nil {
		return nil, fmt.Errorf("deploy: look up linked registry resource %s: %w", registryResourceID, err)
	}
	if resourceType != wantType {
		return nil, fmt.Errorf("deploy: linked registry resource %s is not %s (resource_type=%s)", registryResourceID, wantType, resourceType)
	}
	if status != "applied" {
		return nil, fmt.Errorf("deploy: linked registry resource %s is not applied (status=%s)", registryResourceID, status)
	}
	var outputs map[string]any
	if err := json.Unmarshal([]byte(outputsJSON), &outputs); err != nil {
		return nil, fmt.Errorf("deploy: decode outputs for linked registry resource %s: %w", registryResourceID, err)
	}
	return outputs, nil
}

// ecrAuth fetches a short-lived (12h) ECR authorization token for
// cloudAccountID/region and returns it as a registry.AuthConfig suitable for
// internal/docker.Manager.PushImage — used by deployToECS to push the
// freshly-built image to the deployment's bundled ECR repository.
// repositoryURL is the "ecr_repository_url" terraform output
// (<account>.dkr.ecr.<region>.amazonaws.com/<name>); ServerAddress is the
// registry host portion (before the first "/").
func (o *Orchestrator) ecrAuth(ctx context.Context, cloudAccountID, region, repositoryURL string) (registry.AuthConfig, error) {
	cfg, err := o.loadAWSConfig(ctx, cloudAccountID, region)
	if err != nil {
		return registry.AuthConfig{}, err
	}

	out, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: ecr get authorization token: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: ecr authorization token: no authorization data returned")
	}

	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(out.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: decode ecr authorization token: %w", err)
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return registry.AuthConfig{}, fmt.Errorf("deploy: malformed ecr authorization token")
	}

	serverAddress := repositoryURL
	if idx := strings.Index(serverAddress, "/"); idx != -1 {
		serverAddress = serverAddress[:idx]
	}

	return registry.AuthConfig{
		Username:      user,
		Password:      pass,
		ServerAddress: serverAddress,
	}, nil
}

// acrAuth fetches the admin credentials for the ACR registry described by
// outputs (acr_login_server, resource_group_name — both set by
// templates/azure/stack.tf.tmpl's `.ACR` block) and returns them as a
// registry.AuthConfig — used by deployToACI to push the freshly-built image
// to the deployment's bundled ACR registry. Requires admin_enabled = true on
// the azurerm_container_registry resource (set unconditionally by the
// template), since ListCredentials only works for admin-enabled registries.
func (o *Orchestrator) acrAuth(ctx context.Context, cloudAccountID string, outputs map[string]any) (registry.AuthConfig, error) {
	loginServer := outputString(outputs, "acr_login_server")
	if loginServer == "" {
		return registry.AuthConfig{}, fmt.Errorf("deploy: acr login server not found in resource outputs")
	}
	resourceGroup := outputString(outputs, "resource_group_name")
	if resourceGroup == "" {
		return registry.AuthConfig{}, fmt.Errorf("deploy: resource group not found in resource outputs")
	}

	cred, subscriptionID, err := o.loadAzureCredential(ctx, cloudAccountID)
	if err != nil {
		return registry.AuthConfig{}, err
	}

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, cred, nil)
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: build acr client: %w", err)
	}

	registryName, _, _ := strings.Cut(loginServer, ".")
	creds, err := client.ListCredentials(ctx, resourceGroup, registryName, nil)
	if err != nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: acr list credentials for %s: %w", registryName, err)
	}
	if creds.Username == nil || len(creds.Passwords) == 0 || creds.Passwords[0].Value == nil {
		return registry.AuthConfig{}, fmt.Errorf("deploy: acr %s: no admin credentials returned", registryName)
	}

	return registry.AuthConfig{
		Username:      *creds.Username,
		Password:      *creds.Passwords[0].Value,
		ServerAddress: loginServer,
	}, nil
}
