-- +goose Up

-- Phase 4: "image"-kind apps (e.g. a postgres:16 db container) need a
-- container port to deploy/Service-map and link against, and an optional
-- set of environment variables (e.g. POSTGRES_PASSWORD) encrypted in the
-- vault like git_credentials_secret_id.
ALTER TABLE deployment_apps
    ADD COLUMN port INTEGER CHECK (port IS NULL OR (port > 0 AND port <= 65535)),
    ADD COLUMN image_env_secret_id UUID REFERENCES secrets(id);

-- +goose Down

ALTER TABLE deployment_apps
    DROP COLUMN port,
    DROP COLUMN image_env_secret_id;
