-- +goose Up

-- Compliance findings can now also be scoped to a provisioned resource's
-- terraform workspace (tfsec on generated/imported IaC), alongside app_id
-- (deploy gate) and repo_id (manual repo scan). Exactly one scope is set.
ALTER TABLE compliance_findings
    ADD COLUMN resource_id UUID REFERENCES provisioned_resources(id) ON DELETE CASCADE;

ALTER TABLE compliance_findings DROP CONSTRAINT IF EXISTS compliance_findings_scope_check;
ALTER TABLE compliance_findings
    ADD CONSTRAINT compliance_findings_scope_check CHECK (
        (app_id IS NOT NULL AND repo_id IS NULL AND resource_id IS NULL) OR
        (app_id IS NULL AND repo_id IS NOT NULL AND resource_id IS NULL) OR
        (app_id IS NULL AND repo_id IS NULL AND resource_id IS NOT NULL)
    );

CREATE INDEX ON compliance_findings (resource_id, severity);

-- +goose Down

ALTER TABLE compliance_findings DROP CONSTRAINT IF EXISTS compliance_findings_scope_check;
DELETE FROM compliance_findings WHERE resource_id IS NOT NULL;
ALTER TABLE compliance_findings DROP COLUMN resource_id;
ALTER TABLE compliance_findings
    ADD CONSTRAINT compliance_findings_scope_check CHECK (
        (app_id IS NOT NULL AND repo_id IS NULL) OR
        (app_id IS NULL AND repo_id IS NOT NULL)
    );
