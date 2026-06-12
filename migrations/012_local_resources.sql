-- +goose Up
-- 'local' covers standalone Docker containers / K8s workloads created directly
-- via the Docker SDK / client-go (no Terraform, no cloud_account_id) — Phase 2
-- of the Resources feature.
ALTER TABLE provisioned_resources
    DROP CONSTRAINT provisioned_resources_provider_check,
    ADD CONSTRAINT provisioned_resources_provider_check
        CHECK (provider IN ('aws', 'azure', 'local'));

-- +goose Down
ALTER TABLE provisioned_resources
    DROP CONSTRAINT provisioned_resources_provider_check,
    ADD CONSTRAINT provisioned_resources_provider_check
        CHECK (provider IN ('aws', 'azure'));
