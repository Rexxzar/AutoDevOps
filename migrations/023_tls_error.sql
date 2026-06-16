-- +goose Up
ALTER TABLE deployment_apps ADD COLUMN IF NOT EXISTS tls_error TEXT;

-- +goose Down
ALTER TABLE deployment_apps DROP COLUMN IF EXISTS tls_error;
