// Package cloud polls AWS and Azure for live resource state in connected
// cloud accounts, persisting it to cloud_resources and publishing SSE updates
// — the cloud-side counterpart of internal/k8s's pod-health Watch loop.
package cloud

import "context"

// Provider lists, supplied account credentials, the live resources for one
// cloud account. aws.go and azure.go each implement one.
type Provider interface {
	ListResources(ctx context.Context) ([]Resource, error)
}

// Resource is the provider-agnostic shape persisted to cloud_resources and
// published via SSE — mirrors internal/k8s.PodHealth's "classified state,
// published directly as payload" pattern.
type Resource struct {
	CloudAccountID string         `json:"cloud_account_id"`
	Provider       string         `json:"provider"`
	ResourceType   string         `json:"resource_type"`
	ResourceID     string         `json:"resource_id"`
	Region         string         `json:"region"`
	Name           string         `json:"name,omitempty"`
	State          map[string]any `json:"state"`
}

// Provider and resource-type identifiers stored in cloud_resources and used
// in SSE payloads — kept as plain strings (no central enum), matching how
// sse.Event.Type strings are declared inline across the codebase.
const (
	ProviderAWS   = "aws"
	ProviderAzure = "azure"

	ResourceTypeEC2Instance    = "ec2_instance"
	ResourceTypeVirtualMachine = "virtual_machine"
)

// awsCredentials is the JSON shape stored (encrypted) via vault.Put and read
// back via vault.GetByID using cloud_accounts.credentials_secret_id.
type awsCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region"`
}

// azureCredentials mirrors awsCredentials for Azure service-principal auth.
type azureCredentials struct {
	TenantID       string `json:"tenant_id"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	SubscriptionID string `json:"subscription_id"`
}
