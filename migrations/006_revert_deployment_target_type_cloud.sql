-- +goose Up
-- Undo the 005 placeholder-deployment hack: standalone terraform runs use a
-- NULL deployment_id (already nullable), not a fake 'cloud' deployment row.
UPDATE terraform_runs
SET deployment_id = NULL
WHERE deployment_id IN (SELECT id FROM deployments WHERE repo_url LIKE 'terraform:%');

DELETE FROM deployments WHERE repo_url LIKE 'terraform:%';

ALTER TABLE deployments
    DROP CONSTRAINT deployments_target_type_check,
    ADD CONSTRAINT deployments_target_type_check CHECK (target_type IN ('k8s', 'container', 'vm'));

-- +goose Down
ALTER TABLE deployments
    DROP CONSTRAINT deployments_target_type_check,
    ADD CONSTRAINT deployments_target_type_check CHECK (target_type IN ('k8s', 'container', 'vm', 'cloud'));
