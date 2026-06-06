-- +goose Up
-- Replace the non-unique index with a unique constraint so the Watch loop can upsert
DROP INDEX IF EXISTS pod_health_cluster_id_namespace_pod_name_idx;
ALTER TABLE pod_health ADD CONSTRAINT pod_health_cluster_ns_name_uq
    UNIQUE (cluster_id, namespace, pod_name);

-- +goose Down
ALTER TABLE pod_health DROP CONSTRAINT IF EXISTS pod_health_cluster_ns_name_uq;
CREATE INDEX IF NOT EXISTS pod_health_cluster_id_namespace_pod_name_idx
    ON pod_health (cluster_id, namespace, pod_name);
