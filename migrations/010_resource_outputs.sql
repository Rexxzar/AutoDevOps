-- +goose Up
-- Stores the terraform outputs captured after a successful Apply (vpc_id,
-- public/private IPs, RDS endpoint, db FQDN, etc. — see the `output` blocks
-- added to internal/terraform/templates/*/stack.tf.tmpl) so the resource
-- detail page can show them without re-invoking terraform.
ALTER TABLE provisioned_resources ADD COLUMN outputs JSONB;

-- +goose Down
ALTER TABLE provisioned_resources DROP COLUMN outputs;
