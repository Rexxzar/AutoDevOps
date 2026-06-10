-- +goose Up
-- Lets the dashboard hide finished entries (destroyed provisioned resources,
-- terminated cloud_resources rows) without losing their history — the poller's
-- upsert doesn't touch dismissed_at, so a dismissed cloud_resources row stays
-- hidden across polls until the resource truly disappears and gets pruned.
ALTER TABLE provisioned_resources ADD COLUMN dismissed_at TIMESTAMPTZ;
ALTER TABLE cloud_resources ADD COLUMN dismissed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE cloud_resources DROP COLUMN dismissed_at;
ALTER TABLE provisioned_resources DROP COLUMN dismissed_at;
