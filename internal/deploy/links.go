package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// sanitizeDockerName lower-cases and replaces underscores with hyphens —
// duplicated from internal/docker (same name/behaviour) per the
// namespaceName precedent: small pure helpers are kept in sync manually
// across packages that cannot import each other.
func sanitizeDockerName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// dockerAppHostPort derives the deterministic host port DockerDeploy bound
// container port 8080 to for stackName/appName — duplicated from
// internal/docker's hostPort/appContainerName/AppHostPort (FNV-32a hash of
// "<stack>-<app>" into 8100-8999), needed here so resolveLinks can build a
// host.docker.internal URL for a local_k8s app's local_docker dependency
// without importing internal/docker.
func dockerAppHostPort(stackName, appName string) uint32 {
	cname := sanitizeDockerName(stackName) + "-" + sanitizeDockerName(appName)
	h := fnv.New32a()
	h.Write([]byte(cname))
	return 8100 + (h.Sum32() % 900)
}

// buildURL joins scheme/host/port into a URL string, omitting the port when
// it's the scheme's default (80 for http, 443 for https) — shared by the
// app-link and resource-link branches of resolveLinks.
func buildURL(scheme, host string, port int) string {
	u := fmt.Sprintf("%s://%s", scheme, host)
	if (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
		u = fmt.Sprintf("%s:%d", u, port)
	}
	return u
}

// resolveLinks resolves an app's outgoing dependency links
// (deployment_app_links rows where app_id = appID) into
// AUTODEVOPS_<ALIAS>_{URL,HOST,PORT,USER,PASSWORD,DATABASE} environment
// variables, per the networking rules from the Phase 3 design:
//
//   - same-runtime (k8s->k8s or docker->docker): short name, in-cluster
//     Service port 80, or the per-stack Docker network alias on the
//     dependency's port (8080 for "git" kind apps, the Paketo PORT contract;
//     deployment_apps.port for Phase-4 "image" kind apps, e.g. 5432 for
//     postgres:16)
//   - docker consumer -> k8s dependency: the dependency's Ingress host
//     (https, port 443)
//   - k8s consumer -> docker dependency: host.docker.internal at the
//     dependency's deterministic host port (http)
//
// The k8s->k8s case needs no port distinction between "git" and "image" kind
// dependencies — buildResourceService always maps Service port 80 to the
// container's actual port, whatever it is.
//
// Links to provisioned cloud resources (depends_on_resource_id, Phase 5a) are
// resolved separately by resolveResourceLinks and merged into the result —
// their endpoints (public IP, RDS endpoint, ALB/FQDN) are reachable the same
// way regardless of consumerTargetKind.
func (o *Orchestrator) resolveLinks(ctx context.Context, appID, stackName, consumerTargetKind string) (map[string]string, error) {
	rows, err := o.pool.Query(ctx, `
		SELECT l.alias, da.name, da.target_kind, da.kind, COALESCE(da.port, 0)
		FROM deployment_app_links l
		JOIN deployment_apps da ON da.id = l.depends_on_app_id
		WHERE l.app_id = $1 AND l.depends_on_app_id IS NOT NULL
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("deploy: query links for %s: %w", appID, err)
	}
	defer rows.Close()

	env := make(map[string]string)
	for rows.Next() {
		var alias, depName, depTargetKind, depKind string
		var depPort int
		if err := rows.Scan(&alias, &depName, &depTargetKind, &depKind, &depPort); err != nil {
			return nil, fmt.Errorf("deploy: scan link for %s: %w", appID, err)
		}

		var scheme, host string
		var port int
		switch {
		case consumerTargetKind == "local_k8s" && depTargetKind == "local_k8s":
			scheme, host, port = "http", depName, 80
		case consumerTargetKind == "local_docker" && depTargetKind == "local_docker":
			depPortNum := 8080
			if depKind == "image" && depPort > 0 {
				depPortNum = depPort
			}
			scheme, host, port = "http", depName, depPortNum
		case consumerTargetKind == "local_docker" && depTargetKind == "local_k8s":
			scheme, host, port = "https", depName+"."+o.ingressDomain, 443
		case consumerTargetKind == "local_k8s" && depTargetKind == "local_docker":
			scheme, host, port = "http", "host.docker.internal", int(dockerAppHostPort(stackName, depName))
		case consumerTargetKind == "cloud_vm" && depTargetKind == "local_k8s":
			// Same treatment as a local_docker consumer: reach the k8s app via
			// its public Ingress hostname. local_docker/cloud_vm dependencies
			// are not reachable from a cloud VM in v1 (documented gap).
			scheme, host, port = "https", depName+"."+o.ingressDomain, 443
		default:
			continue
		}

		prefix := "AUTODEVOPS_" + strings.ToUpper(alias) + "_"
		env[prefix+"HOST"] = host
		env[prefix+"PORT"] = fmt.Sprintf("%d", port)
		env[prefix+"URL"] = buildURL(scheme, host, port)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deploy: iterate links for %s: %w", appID, err)
	}

	resourceEnv, err := o.resolveResourceLinks(ctx, appID)
	if err != nil {
		return nil, err
	}
	for k, v := range resourceEnv {
		env[k] = v
	}

	return env, nil
}

// resolveResourceLinks resolves an app's outgoing links to provisioned cloud
// resources (deployment_app_links.depends_on_resource_id, Phase 5a) into
// AUTODEVOPS_<ALIAS>_* environment variables, dispatching on the resource's
// "<provider>:<resource_type>" pair (mirrors internal/console/registry.go's
// dispatch table for the same two columns).
func (o *Orchestrator) resolveResourceLinks(ctx context.Context, appID string) (map[string]string, error) {
	rows, err := o.pool.Query(ctx, `
		SELECT l.alias, pr.provider, pr.resource_type, COALESCE(pr.cloud_account_id::text, ''),
		       pr.region, COALESCE(pr.outputs::text, '{}'), COALESCE(l.port, 0)
		FROM deployment_app_links l
		JOIN provisioned_resources pr ON pr.id = l.depends_on_resource_id
		WHERE l.app_id = $1 AND l.depends_on_resource_id IS NOT NULL
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("deploy: query resource links for %s: %w", appID, err)
	}
	defer rows.Close()

	env := make(map[string]string)
	var devIP string
	var devIPFetched bool
	// fetchDevIP lazily resolves the dev machine's public IP at most once per
	// resolveResourceLinks call, and only when an "aws:rds"/"azure:db" link
	// actually needs it. Best-effort: a failure logs a warning and disables
	// dev-IP injection for the rest of this call, without failing the deploy.
	fetchDevIP := func() string {
		if !devIPFetched {
			devIPFetched = true
			ip, err := getPublicIP(ctx)
			if err != nil {
				slog.Warn("deploy: fetch dev public ip, skipping dev-ip security rule injection", "error", err)
			} else {
				devIP = ip
			}
		}
		return devIP
	}

	for rows.Next() {
		var alias, provider, resourceType, cloudAccountID, region, outputsJSON string
		var linkPort int
		if err := rows.Scan(&alias, &provider, &resourceType, &cloudAccountID, &region, &outputsJSON, &linkPort); err != nil {
			return nil, fmt.Errorf("deploy: scan resource link for %s: %w", appID, err)
		}
		var outputs map[string]any
		if err := json.Unmarshal([]byte(outputsJSON), &outputs); err != nil {
			return nil, fmt.Errorf("deploy: decode outputs for resource link %s: %w", alias, err)
		}

		prefix := "AUTODEVOPS_" + strings.ToUpper(alias) + "_"
		switch provider + ":" + resourceType {
		case "aws:vm":
			setHostPortURL(env, prefix, outputString(outputs, "ec2_public_ip"), linkPort)
		case "azure:vm":
			setHostPortURL(env, prefix, outputString(outputs, "vm_public_ip"), linkPort)
		case "aws:ecs":
			setHostPortURL(env, prefix, outputString(outputs, "ecs_alb_dns_name"), linkPort)
		case "azure:aci":
			setHostPortURL(env, prefix, outputString(outputs, "aci_fqdn"), linkPort)
		case "aws:rds":
			if err := o.resolveRDSLink(ctx, env, prefix, outputs, cloudAccountID, region, linkPort); err != nil {
				return nil, fmt.Errorf("deploy: resolve resource link %s: %w", alias, err)
			}
			if cloudAccountID != "" {
				if ip := fetchDevIP(); ip != "" {
					port := linkPort
					if port == 0 {
						port = outputInt(outputs, "rds_port")
					}
					if err := o.authorizeAWSDevIP(ctx, cloudAccountID, region, outputString(outputs, "rds_security_group_id"), port, ip); err != nil {
						slog.Warn("deploy: authorize dev ip on rds security group", "alias", alias, "error", err)
					}
				}
			}
		case "azure:db":
			if err := o.resolveAzureDBLink(ctx, env, prefix, outputs, cloudAccountID); err != nil {
				return nil, fmt.Errorf("deploy: resolve resource link %s: %w", alias, err)
			}
			if cloudAccountID != "" {
				if ip := fetchDevIP(); ip != "" {
					if err := o.authorizeAzureDevIP(ctx, cloudAccountID, outputs, ip); err != nil {
						slog.Warn("deploy: authorize dev ip on azure postgres firewall", "alias", alias, "error", err)
					}
				}
			}
		default:
			continue
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deploy: iterate resource links for %s: %w", appID, err)
	}

	return env, nil
}

// setHostPortURL sets HOST/PORT/URL for a resource link with a plain
// HTTP endpoint (cloud VM public IP, ECS ALB DNS name, ACI FQDN) — linkPort
// (deployment_app_links.port) overrides the default of 80 when set. A
// missing host (resource not yet applied) leaves env untouched.
func setHostPortURL(env map[string]string, prefix, host string, linkPort int) {
	if host == "" {
		return
	}
	port := linkPort
	if port == 0 {
		port = 80
	}
	env[prefix+"HOST"] = host
	env[prefix+"PORT"] = strconv.Itoa(port)
	env[prefix+"URL"] = buildURL("http", host, port)
}

// resolveRDSLink sets HOST/PORT/USER/DATABASE/PASSWORD/URL for an AWS RDS
// link from its terraform outputs (rds_endpoint, rds_port, rds_username,
// rds_engine, rds_master_user_secret_arn). linkPort overrides rds_port when
// set (e.g. connecting through a different port via a tunnel). The master
// password is fetched from Secrets Manager via rds_master_user_secret_arn
// (manage_master_user_password = true in stack.tf.tmpl) — resources applied
// before this output existed simply get no PASSWORD/credentialed URL.
func (o *Orchestrator) resolveRDSLink(ctx context.Context, env map[string]string, prefix string, outputs map[string]any, cloudAccountID, region string, linkPort int) error {
	host := outputString(outputs, "rds_endpoint")
	if host == "" {
		return nil
	}
	port := linkPort
	if port == 0 {
		port = outputInt(outputs, "rds_port")
	}
	username := outputString(outputs, "rds_username")
	engine := outputString(outputs, "rds_engine")

	env[prefix+"HOST"] = host
	env[prefix+"PORT"] = strconv.Itoa(port)
	if username != "" {
		env[prefix+"USER"] = username
	}

	scheme, database := "mysql", ""
	if strings.HasPrefix(engine, "postgres") {
		scheme, database = "postgres", "postgres"
		env[prefix+"DATABASE"] = database
	}

	var password string
	if secretARN := outputString(outputs, "rds_master_user_secret_arn"); secretARN != "" && cloudAccountID != "" {
		var err error
		password, err = o.rdsMasterPassword(ctx, cloudAccountID, region, secretARN)
		if err != nil {
			return err
		}
		env[prefix+"PASSWORD"] = password
	}

	u := &url.URL{Scheme: scheme, Host: fmt.Sprintf("%s:%d", host, port)}
	if username != "" {
		if password != "" {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	}
	if database != "" {
		u.Path = "/" + database
	}
	env[prefix+"URL"] = u.String()
	return nil
}

// rdsMasterPassword fetches the current RDS master-user password from AWS
// Secrets Manager (the secret managed by manage_master_user_password = true).
func (o *Orchestrator) rdsMasterPassword(ctx context.Context, cloudAccountID, region, secretARN string) (string, error) {
	cfg, err := o.loadAWSConfig(ctx, cloudAccountID, region)
	if err != nil {
		return "", err
	}
	out, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretARN),
	})
	if err != nil {
		return "", fmt.Errorf("get secret value %s: %w", secretARN, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no SecretString", secretARN)
	}
	var secret struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &secret); err != nil {
		return "", fmt.Errorf("decode secret value %s: %w", secretARN, err)
	}
	return secret.Password, nil
}

// resolveAzureDBLink sets HOST/PORT/USER/DATABASE/PASSWORD/URL for an Azure
// Postgres Flexible Server link from its terraform outputs (db_fqdn,
// db_admin_username). stack.tf.tmpl sets password_auth_enabled = false
// (AAD-only), so there is no master-user secret to relay — instead, the
// consuming app authenticates AS the cloud account's own AAD identity
// (db_admin_username == AADAdminPrincipalName): PASSWORD is set to a fresh
// AAD access token for the azurePostgresScope, which Azure Postgres accepts
// directly as the connecting user's password.
//
// Caveat (documented MVP limitation): AAD tokens expire after ~1hr. A token
// baked into a long-lived container's env var will eventually stop working —
// acceptable for single-tenant/demo scope, but a production deployment would
// need the app itself to refresh the token (e.g. via azidentity) rather than
// receiving a static env var.
//
// If cloudAccountID is empty (e.g. a seeded resource with no associated cloud
// account), PASSWORD/credentialed URL are skipped — no live Azure call.
func (o *Orchestrator) resolveAzureDBLink(ctx context.Context, env map[string]string, prefix string, outputs map[string]any, cloudAccountID string) error {
	host := outputString(outputs, "db_fqdn")
	if host == "" {
		return nil
	}
	const port = 5432
	const database = "postgres"

	env[prefix+"HOST"] = host
	env[prefix+"PORT"] = strconv.Itoa(port)
	username := outputString(outputs, "db_admin_username")
	if username != "" {
		env[prefix+"USER"] = username
	}
	env[prefix+"DATABASE"] = database

	var password string
	if cloudAccountID != "" {
		var err error
		password, err = o.azureAADToken(ctx, cloudAccountID)
		if err != nil {
			return err
		}
		env[prefix+"PASSWORD"] = password
	}

	u := &url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", host, port), Path: "/" + database}
	if username != "" {
		if password != "" {
			u.User = url.UserPassword(username, password)
		} else {
			u.User = url.User(username)
		}
	}
	env[prefix+"URL"] = u.String()
	return nil
}

// outputString reads a string-valued terraform output from a
// provisioned_resources.outputs map decoded via json.Unmarshal into
// map[string]any (json.RawMessage strings decode to Go strings).
func outputString(outputs map[string]any, key string) string {
	s, _ := outputs[key].(string)
	return s
}

// outputInt reads a numeric terraform output (decoded as float64 by
// encoding/json) from a provisioned_resources.outputs map.
func outputInt(outputs map[string]any, key string) int {
	switch v := outputs[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}
