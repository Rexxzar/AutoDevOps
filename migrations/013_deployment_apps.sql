-- +goose Up

-- Deployments become "stacks": one deployment can contain N apps, each
-- independently git-built or image-based, each independently targeting
-- local K8s, local Docker, or a pre-provisioned cloud resource (cloud_vm).
CREATE TABLE deployment_apps (
    id                        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id             UUID        NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name                      TEXT        NOT NULL,
    kind                      TEXT        NOT NULL DEFAULT 'git' CHECK (kind IN ('git', 'image')),
    role                      TEXT        CHECK (role IN ('frontend', 'backend', 'db', 'worker', 'other')),
    repo_url                  TEXT,
    branch                    TEXT        NOT NULL DEFAULT 'main',
    framework                 TEXT,
    target_kind               TEXT        NOT NULL CHECK (target_kind IN ('local_k8s', 'local_docker', 'cloud_vm')),
    target_resource_id        UUID        REFERENCES provisioned_resources(id),
    namespace                 TEXT,
    image_ref                 TEXT,
    domain                    TEXT,
    status                    TEXT        NOT NULL DEFAULT 'pending'
                                  CHECK (status IN ('pending','building','deploying','running','failed','destroyed')),
    webhook_secret_id         UUID        REFERENCES secrets(id),
    git_credentials_secret_id UUID        REFERENCES secrets(id),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (deployment_id, name)
);

CREATE INDEX ON deployment_apps (deployment_id);
CREATE INDEX ON deployment_apps (status);

-- Backfill: every existing stack gets one app carrying over its old
-- deployment-level fields. App name matches the stack name.
INSERT INTO deployment_apps (
    deployment_id, name, kind, repo_url, branch, framework, target_kind,
    namespace, image_ref, status, webhook_secret_id, git_credentials_secret_id,
    created_at, updated_at
)
SELECT
    id, name, 'git', repo_url, branch, framework,
    CASE target_type
        WHEN 'k8s'       THEN 'local_k8s'
        WHEN 'container' THEN 'local_docker'
        WHEN 'vm'        THEN 'local_k8s'
        ELSE 'local_k8s'
    END,
    namespace, image_ref, status, webhook_secret_id, git_credentials_secret_id,
    created_at, updated_at
FROM deployments;

-- Explicit dependency links: an app depends on either another app in the
-- same stack, or a pre-provisioned cloud resource.
CREATE TABLE deployment_app_links (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id                 UUID        NOT NULL REFERENCES deployment_apps(id) ON DELETE CASCADE,
    depends_on_app_id      UUID        REFERENCES deployment_apps(id) ON DELETE CASCADE,
    depends_on_resource_id UUID        REFERENCES provisioned_resources(id) ON DELETE CASCADE,
    alias                  TEXT        NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (depends_on_app_id IS NOT NULL AND depends_on_resource_id IS NULL) OR
        (depends_on_app_id IS NULL AND depends_on_resource_id IS NOT NULL)
    ),
    CHECK (app_id <> depends_on_app_id)
);

CREATE INDEX ON deployment_app_links (app_id);

-- Compliance findings are scoped to the app that produced them, not the stack.
ALTER TABLE compliance_findings ADD COLUMN app_id UUID REFERENCES deployment_apps(id) ON DELETE CASCADE;

UPDATE compliance_findings cf
    SET app_id = da.id
    FROM deployment_apps da
    WHERE da.deployment_id = cf.deployment_id;

ALTER TABLE compliance_findings
    ALTER COLUMN app_id SET NOT NULL,
    DROP COLUMN deployment_id;

CREATE INDEX ON compliance_findings (app_id, severity);

-- deployments slims down to a stack container; per-app fields moved to deployment_apps above.
ALTER TABLE deployments
    DROP COLUMN repo_url,
    DROP COLUMN branch,
    DROP COLUMN framework,
    DROP COLUMN target_type,
    DROP COLUMN cluster_id,
    DROP COLUMN namespace,
    DROP COLUMN image_ref,
    DROP COLUMN status,
    DROP COLUMN webhook_secret_id,
    DROP COLUMN git_credentials_secret_id;

-- +goose Down

-- Best-effort schema reversal. For stacks that gained more than one app
-- (post-Phase-1), only the first app's data survives the round trip.
ALTER TABLE deployments
    ADD COLUMN repo_url                  TEXT,
    ADD COLUMN branch                    TEXT NOT NULL DEFAULT 'main',
    ADD COLUMN framework                 TEXT,
    ADD COLUMN target_type               TEXT,
    ADD COLUMN cluster_id                UUID REFERENCES k8s_clusters(id),
    ADD COLUMN namespace                 TEXT,
    ADD COLUMN image_ref                 TEXT,
    ADD COLUMN status                    TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN webhook_secret_id         UUID REFERENCES secrets(id),
    ADD COLUMN git_credentials_secret_id UUID REFERENCES secrets(id);

UPDATE deployments d SET
    repo_url    = da.repo_url,
    branch      = da.branch,
    framework   = da.framework,
    target_type = CASE da.target_kind
        WHEN 'local_k8s'    THEN 'k8s'
        WHEN 'local_docker' THEN 'container'
        WHEN 'cloud_vm'     THEN 'vm'
        ELSE 'k8s'
    END,
    namespace          = da.namespace,
    image_ref          = da.image_ref,
    status             = da.status,
    webhook_secret_id  = da.webhook_secret_id,
    git_credentials_secret_id = da.git_credentials_secret_id
FROM (
    SELECT DISTINCT ON (deployment_id) *
    FROM deployment_apps
    ORDER BY deployment_id, created_at ASC
) da
WHERE da.deployment_id = d.id;

ALTER TABLE deployments
    ALTER COLUMN repo_url SET NOT NULL,
    ALTER COLUMN target_type SET NOT NULL,
    ADD CONSTRAINT deployments_target_type_check CHECK (target_type IN ('k8s', 'container', 'vm')),
    ADD CONSTRAINT deployments_status_check CHECK (status IN ('pending','building','deploying','running','failed','destroyed'));

CREATE INDEX ON deployments (status);

ALTER TABLE compliance_findings ADD COLUMN deployment_id UUID REFERENCES deployments(id) ON DELETE CASCADE;

UPDATE compliance_findings cf
    SET deployment_id = da.deployment_id
    FROM deployment_apps da
    WHERE da.id = cf.app_id;

ALTER TABLE compliance_findings
    DROP COLUMN app_id;

CREATE INDEX ON compliance_findings (deployment_id, severity);

DROP TABLE deployment_app_links;
DROP TABLE deployment_apps;
