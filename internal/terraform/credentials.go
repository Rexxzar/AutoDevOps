package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// awsCredentials and azureCredentials mirror the unexported credential shapes
// in internal/cloud (internal/terraform must not import internal/cloud per
// the package-boundary rules) — both decode the same vault-encrypted
// cloud_accounts.credentials_secret_id JSON blob the cloud poller reads.
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

// providerEnv resolves the environment variables the terraform subprocess
// needs to authenticate against cloudAccountID's provider, decrypted from the
// secrets vault — merged on top of the current process environment, since
// tfexec.SetEnv replaces the child's entire environment rather than extending
// it (losing PATH/TEMP otherwise). Returns nil for an empty cloudAccountID (no
// provider credentials needed) — client (runner.go) falls back to
// processEnv() and adds TF_PLUGIN_CACHE_DIR in that case too.
func (r *Runner) providerEnv(ctx context.Context, cloudAccountID string) (map[string]string, error) {
	if cloudAccountID == "" {
		return nil, nil
	}

	var provider, secretID string
	if err := r.pool.QueryRow(ctx,
		"SELECT provider, credentials_secret_id FROM cloud_accounts WHERE id = $1", cloudAccountID,
	).Scan(&provider, &secretID); err != nil {
		return nil, fmt.Errorf("terraform: look up cloud account %s: %w", cloudAccountID, err)
	}

	plaintext, err := r.vault.GetByID(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("terraform: decrypt cloud account %s credentials: %w", cloudAccountID, err)
	}

	env := processEnv()
	switch provider {
	case string(ProviderAWS):
		var creds awsCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return nil, fmt.Errorf("terraform: decode aws credentials: %w", err)
		}
		env["AWS_ACCESS_KEY_ID"] = creds.AccessKeyID
		env["AWS_SECRET_ACCESS_KEY"] = creds.SecretAccessKey
		env["AWS_DEFAULT_REGION"] = creds.Region
	case string(ProviderAzure):
		var creds azureCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return nil, fmt.Errorf("terraform: decode azure credentials: %w", err)
		}
		env["ARM_SUBSCRIPTION_ID"] = creds.SubscriptionID
		if creds.ClientID != "" {
			env["ARM_TENANT_ID"] = creds.TenantID
			env["ARM_CLIENT_ID"] = creds.ClientID
			env["ARM_CLIENT_SECRET"] = creds.ClientSecret
		}
	default:
		return nil, fmt.Errorf("terraform: unknown cloud provider %q", provider)
	}
	return env, nil
}

// processEnv copies the current process environment into a map, dropping
// TF_-prefixed vars — tfexec.SetEnv rejects those outright (they're set via
// dedicated tfexec options instead) and replaces the child env wholesale
// rather than merging with it.
func processEnv() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.HasPrefix(strings.ToUpper(k), "TF_") {
			continue
		}
		env[k] = v
	}
	return env
}
