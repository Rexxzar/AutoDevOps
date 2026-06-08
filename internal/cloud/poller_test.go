package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Fast tests: pure mapping functions fed synthetic SDK structs, no
// network/credentials/DB required (mirrors internal/terraform's golden-test
// "render and byte-compare, no terraform binary" layer). ---

func TestEC2InstanceToResource(t *testing.T) {
	instance := ec2types.Instance{
		InstanceId:       strPtr("i-0123456789abcdef0"),
		InstanceType:     ec2types.InstanceTypeT3Micro,
		PublicIpAddress:  strPtr("203.0.113.10"),
		PrivateIpAddress: strPtr("10.0.0.5"),
		State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		Tags: []ec2types.Tag{
			{Key: strPtr("Environment"), Value: strPtr("staging")},
			{Key: strPtr("Name"), Value: strPtr("web-1")},
		},
	}

	r := ec2InstanceToResource("acct-1", "eu-west-2", instance)

	if r.CloudAccountID != "acct-1" || r.Provider != ProviderAWS || r.ResourceType != ResourceTypeEC2Instance {
		t.Fatalf("unexpected identity fields: %+v", r)
	}
	if r.ResourceID != "i-0123456789abcdef0" {
		t.Errorf("resource id = %q, want instance id", r.ResourceID)
	}
	if r.Region != "eu-west-2" {
		t.Errorf("region = %q, want %q", r.Region, "eu-west-2")
	}
	if r.Name != "web-1" {
		t.Errorf("name = %q, want the \"Name\" tag value %q", r.Name, "web-1")
	}
	wantState := map[string]any{
		"instance_type": "t3.micro",
		"state":         "running",
		"public_ip":     "203.0.113.10",
		"private_ip":    "10.0.0.5",
	}
	for k, want := range wantState {
		if got := r.State[k]; got != want {
			t.Errorf("state[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestEC2InstanceToResource_NoNameTagOrNetwork(t *testing.T) {
	instance := ec2types.Instance{
		InstanceId:   strPtr("i-noaddr"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		State:        &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
	}

	r := ec2InstanceToResource("acct-1", "eu-west-2", instance)

	if r.Name != "" {
		t.Errorf("name = %q, want empty when no \"Name\" tag is present", r.Name)
	}
	if _, ok := r.State["public_ip"]; ok {
		t.Errorf("state should omit public_ip when the instance has none, got %v", r.State)
	}
	if _, ok := r.State["private_ip"]; ok {
		t.Errorf("state should omit private_ip when the instance has none, got %v", r.State)
	}
	if r.State["state"] != "stopped" {
		t.Errorf(`state["state"] = %v, want "stopped"`, r.State["state"])
	}
}

func TestVirtualMachineToResource(t *testing.T) {
	size := armcompute.VirtualMachineSizeTypesStandardB1S
	vm := &armcompute.VirtualMachine{
		ID:       to.Ptr("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm-1"),
		Name:     to.Ptr("vm-1"),
		Location: to.Ptr("uksouth"),
		Tags:     map[string]*string{"environment": to.Ptr("staging")},
		Properties: &armcompute.VirtualMachineProperties{
			ProvisioningState: to.Ptr("Succeeded"),
			HardwareProfile:   &armcompute.HardwareProfile{VMSize: &size},
		},
	}

	r := virtualMachineToResource("acct-2", vm)

	if r.CloudAccountID != "acct-2" || r.Provider != ProviderAzure || r.ResourceType != ResourceTypeVirtualMachine {
		t.Fatalf("unexpected identity fields: %+v", r)
	}
	if r.ResourceID != *vm.ID {
		t.Errorf("resource id = %q, want %q", r.ResourceID, *vm.ID)
	}
	if r.Region != "uksouth" || r.Name != "vm-1" {
		t.Errorf("region/name = %q/%q, want uksouth/vm-1", r.Region, r.Name)
	}
	if r.State["provisioning_state"] != "Succeeded" {
		t.Errorf(`state["provisioning_state"] = %v, want "Succeeded"`, r.State["provisioning_state"])
	}
	if r.State["vm_size"] != "Standard_B1s" {
		t.Errorf(`state["vm_size"] = %v, want "Standard_B1s"`, r.State["vm_size"])
	}
	tags, ok := r.State["tags"].(map[string]string)
	if !ok || tags["environment"] != "staging" {
		t.Errorf(`state["tags"] = %v, want map with environment=staging`, r.State["tags"])
	}
}

func TestVirtualMachineToResource_NilSafety(t *testing.T) {
	if r := virtualMachineToResource("acct-2", nil); r.ResourceID != "" {
		t.Errorf("nil VM should map to a zero-value resource, got %+v", r)
	}

	bare := &armcompute.VirtualMachine{ID: to.Ptr("/vm-bare")}
	r := virtualMachineToResource("acct-2", bare)
	if r.ResourceID != "/vm-bare" {
		t.Errorf("resource id = %q, want %q", r.ResourceID, "/vm-bare")
	}
	if len(r.State) != 0 {
		t.Errorf("state should be empty when Properties and Tags are nil, got %v", r.State)
	}
}

func TestDerefString(t *testing.T) {
	if got := derefString(nil); got != "" {
		t.Errorf("derefString(nil) = %q, want empty string", got)
	}
	s := "value"
	if got := derefString(&s); got != "value" {
		t.Errorf("derefString(&%q) = %q, want %q", s, got, s)
	}
}

// Credential JSON shapes are persisted (encrypted) via vault.Put and decoded
// back in pollAccount — round-tripping through json guards against field-tag
// drift breaking that decode at runtime.
func TestCredentialJSONRoundTrip(t *testing.T) {
	t.Run("aws", func(t *testing.T) {
		want := awsCredentials{AccessKeyID: "AKIA...", SecretAccessKey: "secret", Region: "eu-west-2"}
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got awsCredentials
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got != want {
			t.Errorf("round-tripped = %+v, want %+v", got, want)
		}
		for _, key := range []string{"access_key_id", "secret_access_key", "region"} {
			if !jsonHasKey(t, raw, key) {
				t.Errorf("encoded JSON missing expected key %q: %s", key, raw)
			}
		}
	})

	t.Run("azure", func(t *testing.T) {
		want := azureCredentials{TenantID: "tenant", ClientID: "client", ClientSecret: "secret", SubscriptionID: "sub"}
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got azureCredentials
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got != want {
			t.Errorf("round-tripped = %+v, want %+v", got, want)
		}
		for _, key := range []string{"tenant_id", "client_id", "client_secret", "subscription_id"} {
			if !jsonHasKey(t, raw, key) {
				t.Errorf("encoded JSON missing expected key %q: %s", key, raw)
			}
		}
	})
}

func jsonHasKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal into map: %v", err)
	}
	_, ok := m[key]
	return ok
}

func strPtr(s string) *string { return &s }

// --- Gated integration test: requires a reachable database AND real test
// credentials for at least one provider. Mirrors the
// AUTODEVOPS_TF_VALIDATE/testPostgres/seedDeployment pattern in
// internal/terraform/runner_test.go — registers a real cloud account through
// the same Vault+DB path the API handler uses, runs one poll cycle, and
// confirms cloud_resources rows land. Optional/manual: there is no AWS/Azure
// account in CI, but the user has free-tier AWS + Azure student credit.
//
// Locally:
//
//	AUTODEVOPS_CLOUD_VALIDATE=1 \
//	AWS_TEST_ACCESS_KEY_ID=... AWS_TEST_SECRET_ACCESS_KEY=... AWS_TEST_REGION=eu-west-2 \
//	go test ./internal/cloud/... -run TestPollerLifecycle -v
func TestPollerLifecycle(t *testing.T) {
	if os.Getenv("AUTODEVOPS_CLOUD_VALIDATE") == "" {
		t.Skip("set AUTODEVOPS_CLOUD_VALIDATE=1 (plus AWS_TEST_*/AZURE_TEST_* credentials) to poll real cloud accounts end to end")
	}

	pool := testPostgres(t)
	vault, err := secrets.New("cloud-poller-test-master-key", pool)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	hub := sse.NewHub()
	poller := NewPoller(pool, hub, vault, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ran := false

	if creds, ok := awsTestCredentials(); ok {
		ran = true
		t.Run("aws", func(t *testing.T) {
			accountID := seedCloudAccount(t, pool, vault, ProviderAWS, "cloud-poller-test-aws", creds)
			pollAndAssertResources(t, ctx, pool, poller, accountID)
		})
	}
	if creds, ok := azureTestCredentials(); ok {
		ran = true
		t.Run("azure", func(t *testing.T) {
			accountID := seedCloudAccount(t, pool, vault, ProviderAzure, "cloud-poller-test-azure", creds)
			pollAndAssertResources(t, ctx, pool, poller, accountID)
		})
	}

	if !ran {
		t.Skip("set AWS_TEST_ACCESS_KEY_ID/AWS_TEST_SECRET_ACCESS_KEY/AWS_TEST_REGION and/or " +
			"AZURE_TEST_TENANT_ID/AZURE_TEST_CLIENT_ID/AZURE_TEST_CLIENT_SECRET/AZURE_TEST_SUBSCRIPTION_ID " +
			"to provide at least one provider's test credentials")
	}
}

func awsTestCredentials() (awsCredentials, bool) {
	c := awsCredentials{
		AccessKeyID:     os.Getenv("AWS_TEST_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_TEST_SECRET_ACCESS_KEY"),
		Region:          os.Getenv("AWS_TEST_REGION"),
	}
	return c, c.AccessKeyID != "" && c.SecretAccessKey != "" && c.Region != ""
}

func azureTestCredentials() (azureCredentials, bool) {
	c := azureCredentials{
		TenantID:       os.Getenv("AZURE_TEST_TENANT_ID"),
		ClientID:       os.Getenv("AZURE_TEST_CLIENT_ID"),
		ClientSecret:   os.Getenv("AZURE_TEST_CLIENT_SECRET"),
		SubscriptionID: os.Getenv("AZURE_TEST_SUBSCRIPTION_ID"),
	}
	return c, c.TenantID != "" && c.ClientID != "" && c.ClientSecret != "" && c.SubscriptionID != ""
}

// pollAndAssertResources runs one full poll cycle (the same pollAll path Run's
// ticker drives) and confirms at least the polled_at bookkeeping for this
// account advanced — accounts may legitimately own zero listable resources,
// so presence of rows isn't asserted, only that polling the account succeeded
// without error and didn't disturb other accounts' rows.
func pollAndAssertResources(t *testing.T, ctx context.Context, pool *pgxpool.Pool, poller *Poller, accountID string) {
	t.Helper()

	var provider, secretID string
	if err := pool.QueryRow(ctx,
		"SELECT provider, credentials_secret_id::text FROM cloud_accounts WHERE id = $1", accountID,
	).Scan(&provider, &secretID); err != nil {
		t.Fatalf("look up seeded cloud account: %v", err)
	}

	if err := poller.pollAccount(ctx, accountID, provider, secretID); err != nil {
		t.Fatalf("pollAccount: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM cloud_resources WHERE cloud_account_id = $1", accountID,
	).Scan(&count); err != nil {
		t.Fatalf("count cloud_resources: %v", err)
	}
	t.Logf("cloud account %s: %d resources persisted after one poll", accountID, count)
}

// testPostgres opens a pool against the dev database, skipping the test (not
// failing it — there's no Postgres service in CI) when one isn't reachable.
// Mirrors internal/terraform/runner_test.go's helper of the same name.
func testPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("cannot create postgres pool for %s: %v", url, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres not reachable at %s — start it with `docker start autodevops-pg`: %v", url, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// seedCloudAccount stores credentials through the Vault and inserts a
// cloud_accounts row exactly like handleCreateCloudAccount does, returning
// its ID. Cleanup removes both rows (cloud_resources cascades via the FK).
func seedCloudAccount(t *testing.T, pool *pgxpool.Pool, vault *secrets.Vault, provider, name string, credentials any) string {
	t.Helper()
	ctx := context.Background()

	raw, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("marshal test credentials: %v", err)
	}

	uniqueName := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	secretName := "cloud-account:" + uniqueName
	if err := vault.Put(ctx, secretName, raw); err != nil {
		t.Fatalf("vault.Put test credentials: %v", err)
	}

	var accountID string
	err = pool.QueryRow(ctx, `
		INSERT INTO cloud_accounts (provider, name, credentials_secret_id)
		SELECT $1, $2, id FROM secrets WHERE name = $3
		RETURNING id::text
	`, provider, uniqueName, secretName).Scan(&accountID)
	if err != nil {
		t.Fatalf("seed test cloud account: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM cloud_accounts WHERE id = $1", accountID); err != nil {
			t.Logf("cleanup test cloud account %s: %v", accountID, err)
		}
		if err := vault.Delete(cleanupCtx, secretName); err != nil {
			t.Logf("cleanup test credentials secret %s: %v", secretName, err)
		}
	})
	return accountID
}
