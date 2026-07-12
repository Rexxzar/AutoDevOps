-- +goose Up

-- Deploy runs: one row per ProcessPush (a git-to-deploy pipeline execution),
-- capturing end-to-end wall-clock deploy latency for the Chapter 5 evaluation.
-- started_at is stamped when the pipeline begins (right after the app lookup, so
-- it covers clone -> build -> compliance -> deploy -> TLS); finished_at + status
-- are stamped when ProcessPush returns. Written best-effort by internal/deploy
-- alongside the deploy, so it never affects the deploy itself.
--
--   duration = finished_at - started_at   (null while still running)
--
-- app_id/deployment_id are ON DELETE SET NULL and app_name/target_kind are
-- denormalised, so measurement rows survive an app or deployment being deleted.
CREATE TABLE deploy_runs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id        UUID        REFERENCES deployment_apps(id) ON DELETE SET NULL,
    deployment_id UUID        REFERENCES deployments(id)     ON DELETE SET NULL,
    app_name      TEXT        NOT NULL DEFAULT '',
    target_kind   TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'in_progress'
                              CHECK (status IN ('in_progress','succeeded','failed')),
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);

CREATE INDEX ON deploy_runs (app_id, started_at DESC);
CREATE INDEX ON deploy_runs (started_at DESC);

-- +goose Down

DROP TABLE deploy_runs;
