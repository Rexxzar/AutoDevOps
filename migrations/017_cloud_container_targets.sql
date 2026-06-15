-- +goose Up

-- Phase 8 (item 1): ECS/ACI as git-app deploy targets, alongside the
-- existing cloud_vm target.
ALTER TABLE deployment_apps
    DROP CONSTRAINT deployment_apps_target_kind_check;

ALTER TABLE deployment_apps
    ADD CONSTRAINT deployment_apps_target_kind_check
    CHECK (target_kind IN ('local_k8s', 'local_docker', 'cloud_vm', 'cloud_ecs', 'cloud_aci'));

-- +goose Down

ALTER TABLE deployment_apps
    DROP CONSTRAINT deployment_apps_target_kind_check;

ALTER TABLE deployment_apps
    ADD CONSTRAINT deployment_apps_target_kind_check
    CHECK (target_kind IN ('local_k8s', 'local_docker', 'cloud_vm'));
