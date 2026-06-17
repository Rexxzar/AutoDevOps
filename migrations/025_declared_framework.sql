-- +goose Up
ALTER TABLE deployment_apps ADD COLUMN declared_framework TEXT;

-- +goose Down
ALTER TABLE deployment_apps DROP COLUMN declared_framework;
