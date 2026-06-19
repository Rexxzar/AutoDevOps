-- +goose Up
-- Per-stack Kubernetes namespace override (NULL/empty = derived autodevops-<stack>).
ALTER TABLE deployments ADD COLUMN namespace_override TEXT;
-- Per-app (git-kind) Kubernetes tuning: replicas, resources, HPA, probes,
-- rolling-update. NULL = the original hardcoded defaults (1 replica, fixed
-- requests/limits, no HPA/probes), so pre-existing apps are unaffected.
ALTER TABLE deployment_apps ADD COLUMN k8s_config JSONB;

-- +goose Down
ALTER TABLE deployment_apps DROP COLUMN k8s_config;
ALTER TABLE deployments DROP COLUMN namespace_override;
