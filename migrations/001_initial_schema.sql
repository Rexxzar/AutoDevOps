-- +goose Up

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- AES-256-GCM encrypted secrets; ciphertext and nonce stored as raw bytes
CREATE TABLE secrets (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL UNIQUE,
    ciphertext BYTEA       NOT NULL,
    nonce      BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE k8s_clusters (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 TEXT        NOT NULL UNIQUE,
    kubeconfig_secret_id UUID        REFERENCES secrets(id),
    endpoint             TEXT        NOT NULL,
    status               TEXT        NOT NULL DEFAULT 'unknown',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE cloud_accounts (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider              TEXT        NOT NULL CHECK (provider IN ('aws', 'azure')),
    name                  TEXT        NOT NULL UNIQUE,
    credentials_secret_id UUID        REFERENCES secrets(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE deployments (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL UNIQUE,
    repo_url    TEXT        NOT NULL,
    branch      TEXT        NOT NULL DEFAULT 'main',
    framework   TEXT,
    target_type TEXT        NOT NULL CHECK (target_type IN ('k8s', 'container', 'vm')),
    cluster_id  UUID        REFERENCES k8s_clusters(id),
    namespace   TEXT,
    image_ref   TEXT,
    status      TEXT        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending','building','deploying','running','failed','destroyed')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE terraform_runs (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id    UUID        REFERENCES deployments(id) ON DELETE CASCADE,
    cloud_account_id UUID        REFERENCES cloud_accounts(id),
    template         TEXT        NOT NULL,
    action           TEXT        NOT NULL CHECK (action IN ('plan', 'apply', 'destroy')),
    status           TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending','running','succeeded','failed')),
    plan_output      TEXT,
    state_file_path  TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE compliance_findings (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID        REFERENCES deployments(id) ON DELETE CASCADE,
    scanner       TEXT        NOT NULL CHECK (scanner IN ('hadolint','trivy','tfsec','gitleaks')),
    severity      TEXT        NOT NULL CHECK (severity IN ('critical','high','medium','low','info')),
    rule_id       TEXT        NOT NULL,
    message       TEXT        NOT NULL,
    file_path     TEXT,
    line_number   INTEGER,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- written by the self-healing MAPE-K controller; one row per observed pod state
CREATE TABLE pod_health (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id      UUID        NOT NULL REFERENCES k8s_clusters(id) ON DELETE CASCADE,
    deployment_id   UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    namespace       TEXT        NOT NULL,
    pod_name        TEXT        NOT NULL,
    status          TEXT        NOT NULL,
    restart_count   INTEGER     NOT NULL DEFAULT 0,
    last_restart_at TIMESTAMPTZ,
    observed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- append-only SSE event log; also the source for PostgreSQL LISTEN/NOTIFY
CREATE TABLE events (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    type       TEXT        NOT NULL,
    payload    JSONB       NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON events              (type);
CREATE INDEX ON events              (created_at DESC);
CREATE INDEX ON pod_health          (cluster_id, namespace, pod_name);
CREATE INDEX ON pod_health          (deployment_id);
CREATE INDEX ON compliance_findings (deployment_id, severity);
CREATE INDEX ON terraform_runs      (deployment_id);
CREATE INDEX ON deployments         (status);

-- +goose Down

DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS pod_health;
DROP TABLE IF EXISTS compliance_findings;
DROP TABLE IF EXISTS terraform_runs;
DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS cloud_accounts;
DROP TABLE IF EXISTS k8s_clusters;
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS users;
