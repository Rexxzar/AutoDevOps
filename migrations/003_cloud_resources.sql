-- +goose Up
CREATE TABLE cloud_resources (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    cloud_account_id UUID        NOT NULL REFERENCES cloud_accounts(id) ON DELETE CASCADE,
    provider         TEXT        NOT NULL CHECK (provider IN ('aws', 'azure')),
    resource_type    TEXT        NOT NULL,
    resource_id      TEXT        NOT NULL,
    region           TEXT        NOT NULL,
    name             TEXT,
    state            JSONB       NOT NULL DEFAULT '{}',
    polled_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cloud_account_id, resource_type, resource_id)
);

CREATE INDEX cloud_resources_cloud_account_id_idx ON cloud_resources (cloud_account_id);

-- +goose Down
DROP TABLE IF EXISTS cloud_resources;
