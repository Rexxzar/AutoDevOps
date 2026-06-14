package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// awsCredentials mirrors the unexported credential shape in internal/cloud,
// internal/terraform and internal/console (internal/deploy must not import
// any of them per the package-boundary rules) — all four decode the
// same vault-encrypted cloud_accounts.credentials_secret_id JSON blob.
type awsCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region"`
}

// loadAWSConfig builds an aws.Config for cloudAccountID/region, suitable for
// secretsmanager.NewFromConfig — used by resolveRDSLink to fetch an RDS
// master-user password for a resource-dependency link.
func (o *Orchestrator) loadAWSConfig(ctx context.Context, cloudAccountID, region string) (aws.Config, error) {
	var secretID string
	if err := o.pool.QueryRow(ctx,
		"SELECT credentials_secret_id FROM cloud_accounts WHERE id = $1", cloudAccountID,
	).Scan(&secretID); err != nil {
		return aws.Config{}, fmt.Errorf("deploy: look up cloud account %s: %w", cloudAccountID, err)
	}
	plaintext, err := o.vault.GetByID(ctx, secretID)
	if err != nil {
		return aws.Config{}, fmt.Errorf("deploy: decrypt cloud account %s credentials: %w", cloudAccountID, err)
	}
	var creds awsCredentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return aws.Config{}, fmt.Errorf("deploy: decode aws credentials: %w", err)
	}
	return awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
}

// azureCredentials mirrors the unexported credential shape in internal/cloud,
// internal/terraform and internal/console (internal/deploy must not import
// any of them per the package-boundary rules) — all four decode the
// same vault-encrypted cloud_accounts.credentials_secret_id JSON blob.
type azureCredentials struct {
	TenantID       string `json:"tenant_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	ClientSecret   string `json:"client_secret,omitempty"`
	SubscriptionID string `json:"subscription_id"`
}

// loadAzureCredential builds an azcore.TokenCredential for cloudAccountID and
// returns it alongside the Azure subscription ID, suitable for
// armcompute.NewVirtualMachinesClient — used by dialAzureVM to push an
// ephemeral SSH key via VM Run Command.
func (o *Orchestrator) loadAzureCredential(ctx context.Context, cloudAccountID string) (azcore.TokenCredential, string, error) {
	var secretID string
	if err := o.pool.QueryRow(ctx,
		"SELECT credentials_secret_id FROM cloud_accounts WHERE id = $1", cloudAccountID,
	).Scan(&secretID); err != nil {
		return nil, "", fmt.Errorf("deploy: look up cloud account %s: %w", cloudAccountID, err)
	}
	plaintext, err := o.vault.GetByID(ctx, secretID)
	if err != nil {
		return nil, "", fmt.Errorf("deploy: decrypt cloud account %s credentials: %w", cloudAccountID, err)
	}
	var creds azureCredentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return nil, "", fmt.Errorf("deploy: decode azure credentials: %w", err)
	}
	if creds.SubscriptionID == "" {
		return nil, "", fmt.Errorf("deploy: azure credentials missing subscription_id")
	}

	var cred azcore.TokenCredential
	if creds.ClientID != "" || creds.ClientSecret != "" || creds.TenantID != "" {
		if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, "", fmt.Errorf("deploy: azure service-principal credentials require tenant_id, client_id and client_secret")
		}
		cred, err = azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	} else {
		cred, err = azidentity.NewAzureCLICredential(nil)
	}
	if err != nil {
		return nil, "", fmt.Errorf("deploy: build azure credential: %w", err)
	}
	return cred, creds.SubscriptionID, nil
}
