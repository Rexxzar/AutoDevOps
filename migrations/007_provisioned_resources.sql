-- +goose Up
-- Standalone cloud resources (Resources tab) — no git deployment involved.
-- One row per "resource" entity; terraform_runs.resource_id groups its
-- plan/apply/destroy history (mirrors deployment_id's role for deployments).
CREATE TABLE provisioned_resources (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT        NOT NULL UNIQUE,
    provider         TEXT        NOT NULL CHECK (provider IN ('aws', 'azure')),
    cloud_account_id UUID        REFERENCES cloud_accounts(id),
    region           TEXT        NOT NULL,
    resource_type    TEXT        NOT NULL,
    template         TEXT        NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'planned', 'applying', 'applied', 'destroying', 'destroyed', 'failed')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE terraform_runs
    ADD COLUMN resource_id UUID REFERENCES provisioned_resources(id) ON DELETE CASCADE;

CREATE INDEX ON terraform_runs (resource_id);
CREATE INDEX ON provisioned_resources (status);

-- +goose Down
ALTER TABLE terraform_runs DROP COLUMN resource_id;
DROP TABLE provisioned_resources;
