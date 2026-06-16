-- +goose Up
ALTER TABLE deployment_apps ADD COLUMN tls_status TEXT NOT NULL DEFAULT 'none';
-- Values: none | pending | provisioning | active | dns_failed | cert_failed | failed

-- +goose Down
ALTER TABLE deployment_apps DROP COLUMN tls_status;
