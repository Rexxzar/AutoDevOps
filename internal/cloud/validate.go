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

// ValidationResult contains details about a successful credential check.
type ValidationResult struct {
	Provider string `json:"provider"`
	Region   string `json:"region,omitempty"`
	Account  string `json:"account,omitempty"`
	UserARN  string `json:"user_arn,omitempty"`
	SubName  string `json:"subscription_name,omitempty"`
	SubID    string `json:"subscription_id,omitempty"`
	AuthMode string `json:"auth_mode,omitempty"`
}

// ValidateCredentials checks that the provided credentials can actually
// authenticate against the cloud provider. For AWS it calls
// sts:GetCallerIdentity; for Azure it fetches the subscription. Returns
// details on success or an error describing what went wrong.
func ValidateCredentials(ctx context.Context, provider string, raw json.RawMessage) (*ValidationResult, error) {
	switch provider {
	case ProviderAWS:
		return validateAWS(ctx, raw)
	case ProviderAzure:
		return validateAzure(ctx, raw)
	default:
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
}

func validateAWS(ctx context.Context, raw json.RawMessage) (*ValidationResult, error) {
	var creds awsCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("invalid AWS credentials JSON: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("access_key_id and secret_access_key are required")
	}
	if creds.Region == "" {
		return nil, fmt.Errorf("region is required")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(creds.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build AWS config: %w", err)
	}

	stsClient := sts.NewFromConfig(cfg)
	out, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("AWS authentication failed: %w", err)
	}

	if out.Account == nil {
		return nil, fmt.Errorf("AWS authentication failed: no account returned")
	}

	result := &ValidationResult{
		Provider: ProviderAWS,
		Region:   creds.Region,
	}
	if out.Account != nil {
		result.Account = *out.Account
	}
	if out.Arn != nil {
		result.UserARN = *out.Arn
	}
	return result, nil
}

func validateAzure(ctx context.Context, raw json.RawMessage) (*ValidationResult, error) {
	var creds azureCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("invalid Azure credentials JSON: %w", err)
	}
	if creds.SubscriptionID == "" {
		return nil, fmt.Errorf("subscription_id is required")
	}

	var cred azcore.TokenCredential
	var err error
	authMode := "az-cli"
	switch {
	case creds.ClientID != "" || creds.ClientSecret != "" || creds.TenantID != "":
		if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, fmt.Errorf("service-principal auth requires all three: tenant_id, client_id, client_secret")
		}
		cred, err = azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
		authMode = "service-principal"
	default:
		cred, err = azidentity.NewAzureCLICredential(nil)
	}
	if err != nil {
		return nil, fmt.Errorf("Azure credential setup failed: %w", err)
	}

	client, err := armsubscription.NewSubscriptionsClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Azure subscriptions client: %w", err)
	}

	resp, err := client.Get(ctx, creds.SubscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("Azure authentication failed: %w", err)
	}

	result := &ValidationResult{
		Provider: ProviderAzure,
		SubID:    creds.SubscriptionID,
		AuthMode: authMode,
	}
	if resp.DisplayName != nil {
		result.SubName = *resp.DisplayName
	}
	return result, nil
}
