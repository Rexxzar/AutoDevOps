-- +goose Up
ALTER TABLE deployments
    DROP CONSTRAINT deployments_target_type_check,
    ADD CONSTRAINT deployments_target_type_check CHECK (target_type IN ('k8s', 'container', 'vm', 'cloud'));

-- +goose Down
ALTER TABLE deployments
    DROP CONSTRAINT deployments_target_type_check,
    ADD CONSTRAINT deployments_target_type_check CHECK (target_type IN ('k8s', 'container', 'vm'));
