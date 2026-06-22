-- +goose Up

-- Git Repo Store: repos become a first-class, reusable entity. A deployment
-- app may be backed by a stored repo (repo_id) but keeps its own pinned
-- repo_url/branch so a repo's later default change never silently retargets a
-- live app (Option 1 — store + pin).
CREATE TABLE git_repos (
    id                        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name                      TEXT        NOT NULL UNIQUE,
    repo_url                  TEXT        NOT NULL,
    branch                    TEXT        NOT NULL DEFAULT 'main',
    git_auth_type             TEXT        CHECK (git_auth_type IN ('token', 'ssh_key')),
    git_credentials_secret_id UUID        REFERENCES secrets(id),
    last_scanned_at           TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON git_repos (repo_url);

-- Provenance link: which stored repo this app came from. SET NULL on repo
-- delete — deleting a repo from the store must never cascade-destroy a live app.
ALTER TABLE deployment_apps
    ADD COLUMN repo_id UUID REFERENCES git_repos(id) ON DELETE SET NULL;

CREATE INDEX ON deployment_apps (repo_id);

-- compliance_findings can now be scoped to EITHER a deployed app (app_id, as
-- before) OR a manual repo-store scan (repo_id). Exactly one is set.
ALTER TABLE compliance_findings
    ALTER COLUMN app_id DROP NOT NULL;

ALTER TABLE compliance_findings
    ADD COLUMN repo_id UUID REFERENCES git_repos(id) ON DELETE CASCADE;

ALTER TABLE compliance_findings
    ADD CONSTRAINT compliance_findings_scope_check CHECK (
        (app_id IS NOT NULL AND repo_id IS NULL) OR
        (app_id IS NULL AND repo_id IS NOT NULL)
    );

CREATE INDEX ON compliance_findings (repo_id, severity);

-- +goose Down

ALTER TABLE compliance_findings DROP CONSTRAINT IF EXISTS compliance_findings_scope_check;
DELETE FROM compliance_findings WHERE repo_id IS NOT NULL;
ALTER TABLE compliance_findings DROP COLUMN repo_id;
ALTER TABLE compliance_findings ALTER COLUMN app_id SET NOT NULL;

ALTER TABLE deployment_apps DROP COLUMN repo_id;

DROP TABLE git_repos;
