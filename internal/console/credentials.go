package console

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/secrets"
)

// awsCredentials and azureCredentials mirror the unexported credential shapes
// in internal/cloud and internal/terraform (internal/console must not import
// either per the package-boundary rules) — all three decode the same
// vault-encrypted cloud_accounts.credentials_secret_id JSON blob.
type awsCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region"`
}

type azureCredentials struct {
	TenantID       string `json:"tenant_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	ClientSecret   string `json:"client_secret,omitempty"`
	SubscriptionID string `json:"subscription_id"`
}

// cloudCredentials looks up cloudAccountID's provider and decrypts its vault
// credentials — same lookup as cloud.ListNetworks / terraform.providerEnv.
func (reg *Registry) cloudCredentials(ctx context.Context, cloudAccountID string) (plaintext []byte, err error) {
	var secretID string
	if err := reg.pool.QueryRow(ctx,
		"SELECT credentials_secret_id FROM cloud_accounts WHERE id = $1", cloudAccountID,
	).Scan(&secretID); err != nil {
		return nil, fmt.Errorf("console: look up cloud account %s: %w", cloudAccountID, err)
	}
	plaintext, err = reg.vault.GetByID(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("console: decrypt cloud account %s credentials: %w", cloudAccountID, err)
	}
	return plaintext, nil
}

// loadAWSConfig builds an aws.Config for cloudAccountID/region, suitable for
// ec2instanceconnect.NewFromConfig.
func (reg *Registry) loadAWSConfig(ctx context.Context, cloudAccountID, region string) (aws.Config, error) {
	plaintext, err := reg.cloudCredentials(ctx, cloudAccountID)
	if err != nil {
		return aws.Config{}, err
	}
	var creds awsCredentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return aws.Config{}, fmt.Errorf("console: decode aws credentials: %w", err)
	}
	return awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
}

// loadAzureCredential builds an azcore.TokenCredential for cloudAccountID and
// returns it alongside the Azure subscription ID, suitable for
// armcompute.NewVirtualMachinesClient.
func (reg *Registry) loadAzureCredential(ctx context.Context, cloudAccountID string) (azcore.TokenCredential, string, error) {
	plaintext, err := reg.cloudCredentials(ctx, cloudAccountID)
	if err != nil {
		return nil, "", err
	}
	var creds azureCredentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return nil, "", fmt.Errorf("console: decode azure credentials: %w", err)
	}
	if creds.SubscriptionID == "" {
		return nil, "", fmt.Errorf("console: azure credentials missing subscription_id")
	}

	var cred azcore.TokenCredential
	if creds.ClientID != "" || creds.ClientSecret != "" || creds.TenantID != "" {
		if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, "", fmt.Errorf("console: azure service-principal credentials require tenant_id, client_id and client_secret")
		}
		cred, err = azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	} else {
		cred, err = azidentity.NewAzureCLICredential(nil)
	}
	if err != nil {
		return nil, "", fmt.Errorf("console: build azure credential: %w", err)
	}
	return cred, creds.SubscriptionID, nil
}

// Registry maps a provisioned resource's (provider, resource_type) to the
// Connector that knows how to open a shell / run commands on it.
type Registry struct {
	pool  *pgxpool.Pool
	vault *secrets.Vault
}

// NewRegistry constructs a Registry. pool and vault are used by connectors to
// decrypt the target's cloud-account credentials on demand at connect time.
func NewRegistry(pool *pgxpool.Pool, vault *secrets.Vault) *Registry {
	return &Registry{pool: pool, vault: vault}
}
