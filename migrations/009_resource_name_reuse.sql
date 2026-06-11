-- +goose Up
-- The original UNIQUE(name) constraint blocks reusing a name forever, even
-- after the resource has been destroyed and dismissed from the dashboard —
-- retrying the New Resource wizard with the same name then fails the INSERT
-- with a raw 23505, surfaced to the user as a generic "failed to register
-- resource". Replace it with a partial unique index that only guards
-- still-visible resources, so a dismissed name becomes available again.
ALTER TABLE provisioned_resources DROP CONSTRAINT provisioned_resources_name_key;
CREATE UNIQUE INDEX provisioned_resources_name_active_idx
    ON provisioned_resources (name) WHERE dismissed_at IS NULL;

-- +goose Down
DROP INDEX provisioned_resources_name_active_idx;
ALTER TABLE provisioned_resources ADD CONSTRAINT provisioned_resources_name_key UNIQUE (name);
