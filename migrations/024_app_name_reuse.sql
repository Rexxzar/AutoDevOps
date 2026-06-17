-- +goose Up

-- Allow a name to be reused within a stack after the previous app with that
-- name has been deleted (status='destroyed') — same pattern as migration 009
-- did for provisioned_resources. Replaces the plain UNIQUE(deployment_id, name)
-- from migration 013 with a partial unique index that only covers live apps.
ALTER TABLE deployment_apps DROP CONSTRAINT deployment_apps_deployment_id_name_key;

CREATE UNIQUE INDEX deployment_apps_name_active_uniq
    ON deployment_apps (deployment_id, name)
    WHERE status != 'destroyed';

-- +goose Down

DROP INDEX deployment_apps_name_active_uniq;

ALTER TABLE deployment_apps ADD CONSTRAINT deployment_apps_deployment_id_name_key
    UNIQUE (deployment_id, name);
