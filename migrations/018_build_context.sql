-- +goose Up

-- Phase 9: docker-compose auto-import maps a compose service's `build:`
-- context (a subdirectory of the repo, e.g. "./backend") onto a `git`-kind
-- app. NULL/empty means "build from the repo root", preserving today's
-- behaviour for every existing app.
ALTER TABLE deployment_apps ADD COLUMN build_context TEXT;

-- +goose Down

ALTER TABLE deployment_apps DROP COLUMN build_context;
