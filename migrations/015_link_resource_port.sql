-- +goose Up

-- Phase 5a: links to a provisioned_resource (e.g. an RDS instance, EC2/Azure
-- VM, ECS service, ACI container group) may need a port distinct from the
-- resource's natural default (e.g. exposing a non-default app port on a VM).
-- Only meaningful for resource links, not app-to-app links.
ALTER TABLE deployment_app_links
    ADD COLUMN port INTEGER CHECK (
        port IS NULL OR (port > 0 AND port <= 65535 AND depends_on_resource_id IS NOT NULL)
    );

-- +goose Down

ALTER TABLE deployment_app_links
    DROP COLUMN port;
