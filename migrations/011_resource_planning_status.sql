-- +goose Up
-- 'planning' covers a re-plan against hand-edited Terraform from the
-- resource detail page (POST /resources/{id}/terraform/plan) — distinct
-- from 'pending' (initial plan after creation, never yet applied).
ALTER TABLE provisioned_resources
    DROP CONSTRAINT provisioned_resources_status_check,
    ADD CONSTRAINT provisioned_resources_status_check
        CHECK (status IN ('pending', 'planning', 'planned', 'applying', 'applied', 'destroying', 'destroyed', 'failed'));

-- +goose Down
ALTER TABLE provisioned_resources
    DROP CONSTRAINT provisioned_resources_status_check,
    ADD CONSTRAINT provisioned_resources_status_check
        CHECK (status IN ('pending', 'planned', 'applying', 'applied', 'destroying', 'destroyed', 'failed'));
