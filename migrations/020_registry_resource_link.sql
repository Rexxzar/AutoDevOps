-- +goose Up

-- Optional link from an ECS/ACI resource to a standalone ECR/ACR registry
-- resource (both provisioned_resources rows) — set when the ECS service /
-- ACI container group should redeploy via that registry (internal/deploy's
-- loadECSResource/loadACIResource resolve ecr_repository_url /
-- acr_login_server from the linked row's outputs, not their own).
ALTER TABLE provisioned_resources
    ADD COLUMN registry_resource_id UUID REFERENCES provisioned_resources(id);

-- +goose Down

ALTER TABLE provisioned_resources
    DROP COLUMN registry_resource_id;
