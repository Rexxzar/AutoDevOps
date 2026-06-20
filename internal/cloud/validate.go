package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
)

// ValidateCredentials checks that the provided credentials can actually
// authenticate against the cloud provider. For AWS it calls
// sts:GetCallerIdentity; for Azure it lists subscriptions. Returns nil on
// success or a user-facing error describing what went wrong.
func ValidateCredentials(ctx context.Context, provider string, raw json.RawMessage) error {
	switch provider {
	case ProviderAWS:
		return validateAWS(ctx, raw)
	case ProviderAzure:
		return validateAzure(ctx, raw)
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

func validateAWS(ctx context.Context, raw json.RawMessage) error {
	var creds awsCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return fmt.Errorf("invalid AWS credentials JSON: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return fmt.Errorf("access_key_id and secret_access_key are required")
	}
	if creds.Region == "" {
		return fmt.Errorf("region is required")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(creds.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
	if err != nil {
		return fmt.Errorf("failed to build AWS config: %w", err)
	}

	stsClient := sts.NewFromConfig(cfg)
	out, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("AWS authentication failed: %w", err)
	}

	if out.Account != nil {
		return nil
	}
	return fmt.Errorf("AWS authentication failed: no account returned")
}

func validateAzure(ctx context.Context, raw json.RawMessage) error {
	var creds azureCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return fmt.Errorf("invalid Azure credentials JSON: %w", err)
	}
	if creds.SubscriptionID == "" {
		return fmt.Errorf("subscription_id is required")
	}

	var cred azcore.TokenCredential
	var err error
	switch {
	case creds.ClientID != "" || creds.ClientSecret != "" || creds.TenantID != "":
		if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" {
			return fmt.Errorf("service-principal auth requires all three: tenant_id, client_id, client_secret")
		}
		cred, err = azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	default:
		cred, err = azidentity.NewAzureCLICredential(nil)
	}
	if err != nil {
		return fmt.Errorf("Azure credential setup failed: %w", err)
	}

	client, err := armsubscription.NewSubscriptionsClient(cred, nil)
	if err != nil {
		return fmt.Errorf("failed to build Azure subscriptions client: %w", err)
	}

	_, err = client.Get(ctx, creds.SubscriptionID, nil)
	if err != nil {
		return fmt.Errorf("Azure authentication failed: %w", err)
	}

	return nil
}
