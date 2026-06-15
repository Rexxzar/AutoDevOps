package deploy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Fast tests: pure functions, no DB/network required. ---

func TestBuildURL(t *testing.T) {
	cases := []struct {
		name   string
		scheme string
		host   string
		port   int
		want   string
	}{
		{"http default port", "http", "example.com", 80, "http://example.com"},
		{"http custom port", "http", "example.com", 8080, "http://example.com:8080"},
		{"https default port", "https", "example.com", 443, "https://example.com"},
		{"https custom port", "https", "example.com", 8443, "https://example.com:8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildURL(tc.scheme, tc.host, tc.port); got != tc.want {
				t.Errorf("buildURL(%q, %q, %d) = %q, want %q", tc.scheme, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestSetHostPortURL(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		linkPort int
		want     map[string]string
	}{
		{"empty host is no-op", "", 8080, map[string]string{}},
		{"default port 80", "1.2.3.4", 0, map[string]string{
			"P_HOST": "1.2.3.4", "P_PORT": "80", "P_URL": "http://1.2.3.4",
		}},
		{"custom port", "1.2.3.4", 8080, map[string]string{
			"P_HOST": "1.2.3.4", "P_PORT": "8080", "P_URL": "http://1.2.3.4:8080",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := make(map[string]string)
			setHostPortURL(env, "P_", tc.host, tc.linkPort)
			if len(env) != len(tc.want) {
				t.Fatalf("env = %v, want %v", env, tc.want)
			}
			for k, v := range tc.want {
				if env[k] != v {
					t.Errorf("env[%q] = %q, want %q", k, env[k], v)
				}
			}
		})
	}
}

func TestOutputStringAndOutputInt(t *testing.T) {
	outputs := map[string]any{
		"str":       "hello",
		"num_float": float64(5432),
		"num_str":   "5433",
		"bool":      true,
	}
	if got := outputString(outputs, "str"); got != "hello" {
		t.Errorf("outputString(str) = %q, want hello", got)
	}
	if got := outputString(outputs, "missing"); got != "" {
		t.Errorf("outputString(missing) = %q, want empty", got)
	}
	if got := outputInt(outputs, "num_float"); got != 5432 {
		t.Errorf("outputInt(num_float) = %d, want 5432", got)
	}
	if got := outputInt(outputs, "num_str"); got != 5433 {
		t.Errorf("outputInt(num_str) = %d, want 5433", got)
	}
	if got := outputInt(outputs, "bool"); got != 0 {
		t.Errorf("outputInt(bool) = %d, want 0", got)
	}
	if got := outputInt(outputs, "missing"); got != 0 {
		t.Errorf("outputInt(missing) = %d, want 0", got)
	}
}

func TestResolveAzureDBLink(t *testing.T) {
	o := &Orchestrator{}
	ctx := context.Background()

	t.Run("with fqdn and username", func(t *testing.T) {
		env := make(map[string]string)
		if err := o.resolveAzureDBLink(ctx, env, "DB_", map[string]any{
			"db_fqdn":           "myserver.postgres.database.azure.com",
			"db_admin_username": "azureadmin",
		}, ""); err != nil {
			t.Fatalf("resolveAzureDBLink() error = %v", err)
		}
		want := map[string]string{
			"DB_HOST":     "myserver.postgres.database.azure.com",
			"DB_PORT":     "5432",
			"DB_USER":     "azureadmin",
			"DB_DATABASE": "postgres",
			"DB_URL":      "postgres://azureadmin@myserver.postgres.database.azure.com:5432/postgres",
		}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("env[%q] = %q, want %q", k, env[k], v)
			}
		}
		if _, ok := env["DB_PASSWORD"]; ok {
			t.Error("DB_PASSWORD should not be set when cloudAccountID is empty (no live AAD call)")
		}
	})

	t.Run("missing fqdn is no-op", func(t *testing.T) {
		env := make(map[string]string)
		if err := o.resolveAzureDBLink(ctx, env, "DB_", map[string]any{}, ""); err != nil {
			t.Fatalf("resolveAzureDBLink() error = %v", err)
		}
		if len(env) != 0 {
			t.Errorf("env = %v, want empty", env)
		}
	})
}

// TestAuthorizeAWSDevIPNoOp covers the short-circuits that let
// resolveResourceLinks call authorizeAWSDevIP unconditionally without an AWS
// credential or network access in tests: a missing security group ID or port
// skips loadAWSConfig entirely.
func TestAuthorizeAWSDevIPNoOp(t *testing.T) {
	o := &Orchestrator{}
	ctx := context.Background()

	if err := o.authorizeAWSDevIP(ctx, "", "us-east-1", "", 5432, "203.0.113.1"); err != nil {
		t.Errorf("missing security group: error = %v, want nil", err)
	}
	if err := o.authorizeAWSDevIP(ctx, "", "us-east-1", "sg-123", 0, "203.0.113.1"); err != nil {
		t.Errorf("missing port: error = %v, want nil", err)
	}
}

// TestAuthorizeAzureDevIPNoOp covers the short-circuit that lets
// resolveResourceLinks call authorizeAzureDevIP unconditionally without an
// Azure credential or network access in tests: outputs missing
// resource_group_name/db_fqdn skip loadAzureCredential entirely.
func TestAuthorizeAzureDevIPNoOp(t *testing.T) {
	o := &Orchestrator{}
	ctx := context.Background()

	if err := o.authorizeAzureDevIP(ctx, "", map[string]any{}, "203.0.113.1"); err != nil {
		t.Errorf("missing outputs: error = %v, want nil", err)
	}
	if err := o.authorizeAzureDevIP(ctx, "", map[string]any{"resource_group_name": "rg"}, "203.0.113.1"); err != nil {
		t.Errorf("missing db_fqdn: error = %v, want nil", err)
	}
}

func TestResolveRDSLinkNoSecret(t *testing.T) {
	o := &Orchestrator{}
	ctx := context.Background()

	t.Run("postgres engine, no secret arn", func(t *testing.T) {
		env := make(map[string]string)
		outputs := map[string]any{
			"rds_endpoint": "mydb.abc123.us-east-1.rds.amazonaws.com",
			"rds_port":     float64(5432),
			"rds_username": "postgres",
			"rds_engine":   "postgres",
		}
		if err := o.resolveRDSLink(ctx, env, "DB_", outputs, "", "us-east-1", 0); err != nil {
			t.Fatalf("resolveRDSLink: %v", err)
		}
		if env["DB_HOST"] != "mydb.abc123.us-east-1.rds.amazonaws.com" {
			t.Errorf("DB_HOST = %q", env["DB_HOST"])
		}
		if env["DB_PORT"] != "5432" {
			t.Errorf("DB_PORT = %q", env["DB_PORT"])
		}
		if env["DB_USER"] != "postgres" {
			t.Errorf("DB_USER = %q", env["DB_USER"])
		}
		if env["DB_DATABASE"] != "postgres" {
			t.Errorf("DB_DATABASE = %q", env["DB_DATABASE"])
		}
		if _, ok := env["DB_PASSWORD"]; ok {
			t.Error("DB_PASSWORD should not be set without a secret ARN")
		}
		wantURL := "postgres://postgres@mydb.abc123.us-east-1.rds.amazonaws.com:5432/postgres"
		if env["DB_URL"] != wantURL {
			t.Errorf("DB_URL = %q, want %q", env["DB_URL"], wantURL)
		}
	})

	t.Run("mysql engine, link port override", func(t *testing.T) {
		env := make(map[string]string)
		outputs := map[string]any{
			"rds_endpoint": "mydb.abc123.us-east-1.rds.amazonaws.com",
			"rds_port":     float64(3306),
			"rds_username": "admin",
			"rds_engine":   "mysql",
		}
		if err := o.resolveRDSLink(ctx, env, "DB_", outputs, "", "us-east-1", 13306); err != nil {
			t.Fatalf("resolveRDSLink: %v", err)
		}
		if env["DB_PORT"] != "13306" {
			t.Errorf("DB_PORT = %q, want 13306 (link port override)", env["DB_PORT"])
		}
		if _, ok := env["DB_DATABASE"]; ok {
			t.Error("DB_DATABASE should not be set for mysql")
		}
		wantURL := "mysql://admin@mydb.abc123.us-east-1.rds.amazonaws.com:13306"
		if env["DB_URL"] != wantURL {
			t.Errorf("DB_URL = %q, want %q", env["DB_URL"], wantURL)
		}
	})

	t.Run("missing endpoint is no-op", func(t *testing.T) {
		env := make(map[string]string)
		if err := o.resolveRDSLink(ctx, env, "DB_", map[string]any{}, "", "us-east-1", 0); err != nil {
			t.Fatalf("resolveRDSLink: %v", err)
		}
		if len(env) != 0 {
			t.Errorf("env = %v, want empty", env)
		}
	})
}

// --- DB-backed tests: real Postgres (via testPostgres), no cloud
// credentials required. Mirrors seedDeployment's pattern of registering rows
// the same way the API handlers would. ---

// seedLinkTestApp registers a one-app stack (target_kind local_docker) and
// returns the app's ID plus a cleanup that deletes the stack (cascades to
// deployment_apps and deployment_app_links).
func seedLinkTestApp(t *testing.T, pool *pgxpool.Pool) (string, func()) {
	t.Helper()
	ctx := context.Background()

	name := fmt.Sprintf("link-test-app-%d", time.Now().UnixNano())
	var deploymentID, appID string
	err := pool.QueryRow(ctx, `
		WITH stack AS (
			INSERT INTO deployments (name) VALUES ($1) RETURNING id
		)
		INSERT INTO deployment_apps (deployment_id, name, target_kind)
		SELECT stack.id, $1, 'local_docker'
		FROM stack
		RETURNING deployment_id::text, id::text
	`, name).Scan(&deploymentID, &appID)
	if err != nil {
		t.Fatalf("seed link test app: %v", err)
	}

	return appID, func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM deployments WHERE id = $1", deploymentID); err != nil {
			t.Logf("cleanup link test deployment %s: %v", deploymentID, err)
		}
	}
}

// seedAppliedResource inserts a provisioned_resources row with status
// 'applied' and the given terraform outputs (as a JSON object), returning
// its ID plus a cleanup that deletes it (cascades any deployment_app_links
// referencing it).
func seedAppliedResource(t *testing.T, pool *pgxpool.Pool, alias, provider, resourceType, region, outputsJSON string) (string, func()) {
	t.Helper()
	ctx := context.Background()

	name := fmt.Sprintf("link-test-%s-%d", alias, time.Now().UnixNano())
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO provisioned_resources (name, provider, region, resource_type, template, status, outputs)
		VALUES ($1, $2, $3, $4, '# test', 'applied', $5::jsonb)
		RETURNING id::text
	`, name, provider, region, resourceType, outputsJSON).Scan(&id)
	if err != nil {
		t.Fatalf("seed provisioned resource %s: %v", alias, err)
	}

	return id, func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM provisioned_resources WHERE id = $1", id); err != nil {
			t.Logf("cleanup provisioned resource %s: %v", id, err)
		}
	}
}

// seedAppResourceLink inserts a deployment_app_links row pointing appID at
// resourceID, with the given alias and optional port (0 stores NULL).
func seedAppResourceLink(t *testing.T, pool *pgxpool.Pool, appID, resourceID, alias string, linkPort int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO deployment_app_links (app_id, depends_on_resource_id, alias, port)
		VALUES ($1, $2, $3, NULLIF($4, 0))
	`, appID, resourceID, alias, linkPort)
	if err != nil {
		t.Fatalf("seed app resource link %s: %v", alias, err)
	}
}

// TestResolveResourceLinks seeds one provisioned_resources row per
// provider:resource_type pair resolveResourceLinks dispatches on (plus one
// unmapped "local:k8s" pair), links each to a single app, and asserts the
// resulting AUTODEVOPS_<ALIAS>_* env vars. The aws:rds case omits
// rds_master_user_secret_arn so the Secrets Manager path (which needs real
// AWS credentials) is never reached.
func TestResolveResourceLinks(t *testing.T) {
	pool := testPostgres(t)
	ctx := context.Background()
	o := &Orchestrator{pool: pool}

	appID, cleanupApp := seedLinkTestApp(t, pool)
	defer cleanupApp()

	cases := []struct {
		alias        string
		provider     string
		resourceType string
		region       string
		outputs      string
		linkPort     int
		want         map[string]string
		wantAbsent   []string
	}{
		{
			alias: "VM", provider: "aws", resourceType: "vm", region: "us-east-1",
			outputs: `{"ec2_public_ip": "1.2.3.4"}`, linkPort: 3000,
			want: map[string]string{
				"AUTODEVOPS_VM_HOST": "1.2.3.4",
				"AUTODEVOPS_VM_PORT": "3000",
				"AUTODEVOPS_VM_URL":  "http://1.2.3.4:3000",
			},
		},
		{
			alias: "AZVM", provider: "azure", resourceType: "vm", region: "norwayeast",
			outputs: `{"vm_public_ip": "5.6.7.8"}`, linkPort: 0,
			want: map[string]string{
				"AUTODEVOPS_AZVM_HOST": "5.6.7.8",
				"AUTODEVOPS_AZVM_PORT": "80",
				"AUTODEVOPS_AZVM_URL":  "http://5.6.7.8",
			},
		},
		{
			alias: "ECS", provider: "aws", resourceType: "ecs", region: "us-east-1",
			outputs: `{"ecs_alb_dns_name": "my-alb.us-east-1.elb.amazonaws.com"}`, linkPort: 8080,
			want: map[string]string{
				"AUTODEVOPS_ECS_HOST": "my-alb.us-east-1.elb.amazonaws.com",
				"AUTODEVOPS_ECS_PORT": "8080",
				"AUTODEVOPS_ECS_URL":  "http://my-alb.us-east-1.elb.amazonaws.com:8080",
			},
		},
		{
			alias: "ACI", provider: "azure", resourceType: "aci", region: "norwayeast",
			outputs: `{"aci_fqdn": "mycontainer.norwayeast.azurecontainer.io"}`, linkPort: 0,
			want: map[string]string{
				"AUTODEVOPS_ACI_HOST": "mycontainer.norwayeast.azurecontainer.io",
				"AUTODEVOPS_ACI_PORT": "80",
				"AUTODEVOPS_ACI_URL":  "http://mycontainer.norwayeast.azurecontainer.io",
			},
		},
		{
			alias: "AZDB", provider: "azure", resourceType: "db", region: "norwayeast",
			outputs: `{"db_fqdn": "myserver.postgres.database.azure.com", "db_admin_username": "azureadmin"}`, linkPort: 0,
			want: map[string]string{
				"AUTODEVOPS_AZDB_HOST":     "myserver.postgres.database.azure.com",
				"AUTODEVOPS_AZDB_PORT":     "5432",
				"AUTODEVOPS_AZDB_USER":     "azureadmin",
				"AUTODEVOPS_AZDB_DATABASE": "postgres",
				"AUTODEVOPS_AZDB_URL":      "postgres://azureadmin@myserver.postgres.database.azure.com:5432/postgres",
			},
			wantAbsent: []string{"AUTODEVOPS_AZDB_PASSWORD"},
		},
		{
			alias: "RDS", provider: "aws", resourceType: "rds", region: "us-east-1",
			outputs: `{"rds_endpoint": "mydb.abc.us-east-1.rds.amazonaws.com", "rds_port": 5432, "rds_username": "postgres", "rds_engine": "postgres"}`, linkPort: 0,
			want: map[string]string{
				"AUTODEVOPS_RDS_HOST":     "mydb.abc.us-east-1.rds.amazonaws.com",
				"AUTODEVOPS_RDS_PORT":     "5432",
				"AUTODEVOPS_RDS_USER":     "postgres",
				"AUTODEVOPS_RDS_DATABASE": "postgres",
				"AUTODEVOPS_RDS_URL":      "postgres://postgres@mydb.abc.us-east-1.rds.amazonaws.com:5432/postgres",
			},
			wantAbsent: []string{"AUTODEVOPS_RDS_PASSWORD"},
		},
		{
			alias: "K8S", provider: "local", resourceType: "k8s", region: "local",
			outputs: `{}`, linkPort: 0,
			want:       map[string]string{},
			wantAbsent: []string{"AUTODEVOPS_K8S_HOST", "AUTODEVOPS_K8S_PORT", "AUTODEVOPS_K8S_URL"},
		},
	}

	for _, tc := range cases {
		resourceID, cleanup := seedAppliedResource(t, pool, tc.alias, tc.provider, tc.resourceType, tc.region, tc.outputs)
		defer cleanup()
		seedAppResourceLink(t, pool, appID, resourceID, tc.alias, tc.linkPort)
	}

	env, err := o.resolveResourceLinks(ctx, appID)
	if err != nil {
		t.Fatalf("resolveResourceLinks: %v", err)
	}

	for _, tc := range cases {
		for k, v := range tc.want {
			if env[k] != v {
				t.Errorf("[%s] env[%q] = %q, want %q", tc.alias, k, env[k], v)
			}
		}
		for _, k := range tc.wantAbsent {
			if v, ok := env[k]; ok {
				t.Errorf("[%s] env[%q] = %q, want absent", tc.alias, k, v)
			}
		}
	}
}

// TestResolveLinksMergesResourceLinks confirms resolveLinks (the top-level
// entry point) merges resolveResourceLinks's output into its result.
func TestResolveLinksMergesResourceLinks(t *testing.T) {
	pool := testPostgres(t)
	ctx := context.Background()
	o := &Orchestrator{pool: pool, ingressDomain: "apps.127.0.0.1.sslip.io"}

	appID, cleanupApp := seedLinkTestApp(t, pool)
	defer cleanupApp()

	resourceID, cleanupRes := seedAppliedResource(t, pool, "WEB", "aws", "vm", "us-east-1", `{"ec2_public_ip": "9.9.9.9"}`)
	defer cleanupRes()
	seedAppResourceLink(t, pool, appID, resourceID, "WEB", 0)

	env, err := o.resolveLinks(ctx, appID, "link-test-stack", "local_docker")
	if err != nil {
		t.Fatalf("resolveLinks: %v", err)
	}
	if env["AUTODEVOPS_WEB_HOST"] != "9.9.9.9" {
		t.Errorf("AUTODEVOPS_WEB_HOST = %q, want 9.9.9.9", env["AUTODEVOPS_WEB_HOST"])
	}
	if env["AUTODEVOPS_WEB_PORT"] != "80" {
		t.Errorf("AUTODEVOPS_WEB_PORT = %q, want 80", env["AUTODEVOPS_WEB_PORT"])
	}
	if env["AUTODEVOPS_WEB_URL"] != "http://9.9.9.9" {
		t.Errorf("AUTODEVOPS_WEB_URL = %q, want http://9.9.9.9", env["AUTODEVOPS_WEB_URL"])
	}
}
