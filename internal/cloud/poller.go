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

	var prov Provider
	switch provider {
	case ProviderAWS:
		var creds awsCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return fmt.Errorf("cloud: decode aws credentials: %w", err)
		}
		prov, err = newAWSProvider(ctx, cloudAccountID, creds)
	case ProviderAzure:
		var creds azureCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return fmt.Errorf("cloud: decode azure credentials: %w", err)
		}
		prov, err = newAzureProvider(cloudAccountID, creds)
	default:
		return fmt.Errorf("cloud: unknown provider %q", provider)
	}
	if err != nil {
		return fmt.Errorf("cloud: build provider: %w", err)
	}

	resources, err := prov.ListResources(ctx)
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
