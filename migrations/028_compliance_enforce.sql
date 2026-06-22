-- +goose Up

-- Compliance gate is report-only by default: it always scans, records findings,
-- and surfaces them, but only BLOCKS a deploy when enforcement is enabled for
-- the app. Default false so standard base-image CVEs don't block normal deploys.
ALTER TABLE deployment_apps
    ADD COLUMN compliance_enforce BOOLEAN NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE deployment_apps DROP COLUMN compliance_enforce;
