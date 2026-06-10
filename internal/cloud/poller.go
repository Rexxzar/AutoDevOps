package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Poller periodically lists live resources for every registered cloud account,
// persisting them to cloud_resources and publishing SSE updates — the cloud-side
// counterpart of internal/k8s's event-driven pod-health Watch loop. Cloud SDKs
// have no Watch/informer equivalent, so this is the one genuinely polling loop
// in the codebase.
type Poller struct {
	pool     *pgxpool.Pool
	hub      *sse.Hub
	vault    *secrets.Vault
	interval time.Duration
}

// NewPoller builds a Poller. interval controls how often every registered
// cloud account is re-listed.
func NewPoller(pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault, interval time.Duration) *Poller {
	return &Poller{pool: pool, hub: hub, vault: vault, interval: interval}
}

// Run polls every account on a ticker until ctx is cancelled. A bad credential
// or unreachable provider for one account is logged and skipped — it must never
// halt polling for every other account.
func (p *Poller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	slog.Info("cloud: poller running", "interval", p.interval)
	p.pollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.pollAll(ctx)
		}
	}
}

func (p *Poller) pollAll(ctx context.Context) {
	rows, err := p.pool.Query(ctx, "SELECT id, provider, credentials_secret_id FROM cloud_accounts")
	if err != nil {
		slog.Error("cloud: list cloud accounts", "error", err)
		return
	}
	type account struct {
		id, provider, secretID string
	}
	var accounts []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.id, &a.provider, &a.secretID); err != nil {
			rows.Close()
			slog.Error("cloud: scan cloud account", "error", err)
			return
		}
		accounts = append(accounts, a)
	}
	rows.Close()

	for _, a := range accounts {
		if err := p.pollAccount(ctx, a.id, a.provider, a.secretID); err != nil {
			slog.Error("cloud: poll account", "cloud_account_id", a.id, "provider", a.provider, "error", err)
		}
	}
}

func (p *Poller) pollAccount(ctx context.Context, cloudAccountID, provider, secretID string) error {
	plaintext, err := p.vault.GetByID(ctx, secretID)
	if err != nil {
		return fmt.Errorf("cloud: decrypt credentials: %w", err)
	}

	var resources []Resource
	switch provider {
	case ProviderAWS:
		var creds awsCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return fmt.Errorf("cloud: decode aws credentials: %w", err)
		}
		resources, err = p.listAWSResources(ctx, cloudAccountID, creds)
	case ProviderAzure:
		var creds azureCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return fmt.Errorf("cloud: decode azure credentials: %w", err)
		}
		var prov Provider
		prov, err = newAzureProvider(cloudAccountID, creds)
		if err == nil {
			resources, err = prov.ListResources(ctx)
		}
	default:
		return fmt.Errorf("cloud: unknown provider %q", provider)
	}
	if err != nil {
		return fmt.Errorf("cloud: list resources: %w", err)
	}

	for _, r := range resources {
		if err := p.upsertResource(ctx, r); err != nil {
			slog.Error("cloud: upsert resource", "cloud_account_id", cloudAccountID, "resource_id", r.ResourceID, "error", err)
			continue
		}
		p.hub.Publish(sse.Event{Type: "cloud.resource", Payload: r})
	}

	if err := p.pruneStaleResources(ctx, cloudAccountID, resources); err != nil {
		slog.Error("cloud: prune stale resources", "cloud_account_id", cloudAccountID, "error", err)
	}
	return nil
}

// listAWSResources lists EC2/RDS/ECS resources across the account's home
// region (creds.Region) plus every region AutoDevOps has provisioned an AWS
// resource into for this account. The AWS SDK has no "list across all
// regions" call, and polling all ~30 partitions every tick would be wasteful
// — provisioned_resources is the source of truth for which regions
// AutoDevOps itself has put resources in.
func (p *Poller) listAWSResources(ctx context.Context, cloudAccountID string, creds awsCredentials) ([]Resource, error) {
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.Region == "" {
		return nil, fmt.Errorf("cloud: aws credentials require access_key_id, secret_access_key and region")
	}

	regions, err := p.awsRegions(ctx, cloudAccountID, creds.Region)
	if err != nil {
		return nil, err
	}

	var resources []Resource
	for _, region := range regions {
		prov, err := newAWSProvider(ctx, cloudAccountID, region, creds)
		if err != nil {
			return nil, fmt.Errorf("cloud: build provider for %s: %w", region, err)
		}
		rs, err := prov.ListResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: list aws resources in %s: %w", region, err)
		}
		resources = append(resources, rs...)
	}
	return resources, nil
}

// awsRegions returns homeRegion plus every distinct region AutoDevOps has
// provisioned an AWS resource into for cloudAccountID, deduplicated.
func (p *Poller) awsRegions(ctx context.Context, cloudAccountID, homeRegion string) ([]string, error) {
	rows, err := p.pool.Query(ctx,
		"SELECT DISTINCT region FROM provisioned_resources WHERE cloud_account_id = $1 AND provider = 'aws'",
		cloudAccountID,
	)
	if err != nil {
		return nil, fmt.Errorf("cloud: list provisioned regions: %w", err)
	}
	defer rows.Close()

	regions := map[string]struct{}{homeRegion: {}}
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			return nil, fmt.Errorf("cloud: scan provisioned region: %w", err)
		}
		regions[region] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloud: list provisioned regions: %w", err)
	}

	out := make([]string, 0, len(regions))
	for region := range regions {
		out = append(out, region)
	}
	return out, nil
}

// pruneStaleResources deletes cloud_resources rows for cloudAccountID that
// the latest poll no longer reports — e.g. a resource destroyed via terraform
// (or directly in the cloud console). Without this, destroyed resources stay
// in the Cloud Inventory forever with a frozen polled_at.
func (p *Poller) pruneStaleResources(ctx context.Context, cloudAccountID string, current []Resource) error {
	types := make([]string, len(current))
	ids := make([]string, len(current))
	for i, r := range current {
		types[i] = r.ResourceType
		ids[i] = r.ResourceID
	}

	rows, err := p.pool.Query(ctx, `
		DELETE FROM cloud_resources
		WHERE cloud_account_id = $1
		AND (resource_type, resource_id) NOT IN (
			SELECT * FROM unnest($2::text[], $3::text[])
		)
		RETURNING resource_type, resource_id
	`, cloudAccountID, types, ids)
	if err != nil {
		return fmt.Errorf("cloud: prune stale resources: %w", err)
	}
	defer rows.Close()

	var pruned bool
	for rows.Next() {
		pruned = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cloud: prune stale resources: %w", err)
	}

	if pruned {
		p.hub.Publish(sse.Event{Type: "cloud.resource", Payload: map[string]string{"cloud_account_id": cloudAccountID}})
	}
	return nil
}

func (p *Poller) upsertResource(ctx context.Context, r Resource) error {
	state, err := json.Marshal(r.State)
	if err != nil {
		return fmt.Errorf("cloud: marshal resource state: %w", err)
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO cloud_resources
			(cloud_account_id, provider, resource_type, resource_id, region, name, state, polled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (cloud_account_id, resource_type, resource_id) DO UPDATE SET
			region    = EXCLUDED.region,
			name      = EXCLUDED.name,
			state     = EXCLUDED.state,
			polled_at = now()
	`, r.CloudAccountID, r.Provider, r.ResourceType, r.ResourceID, r.Region, r.Name, state)
	if err != nil {
		return fmt.Errorf("cloud: upsert resource: %w", err)
	}
	return nil
}
