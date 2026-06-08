-- +goose Up
ALTER TABLE deployments
    ADD COLUMN webhook_secret_id         UUID REFERENCES secrets(id),
    ADD COLUMN git_credentials_secret_id UUID REFERENCES secrets(id);

-- +goose Down
ALTER TABLE deployments
    DROP COLUMN webhook_secret_id,
    DROP COLUMN git_credentials_secret_id;
