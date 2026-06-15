-- +goose Up

-- Phase 4 (item 7): optional persistent storage path for "image"-kind apps
-- (e.g. /var/lib/postgresql/data for postgres:16) — backed by a Docker named
-- volume for local_docker targets, or a PVC against kind's "standard"
-- local-path-provisioner StorageClass for local_k8s targets.
ALTER TABLE deployment_apps
    ADD COLUMN volume_path TEXT;

-- +goose Down

ALTER TABLE deployment_apps
    DROP COLUMN volume_path;
