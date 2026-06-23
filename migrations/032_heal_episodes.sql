-- +goose Up

-- Self-healing episodes: one row per "a target went unhealthy -> recovered (or
-- the healer gave up)" cycle, for both K8s pods and Docker containers. Powers
-- the MTTD/MTTR evaluation (T21) and a metrics card in the dashboard. Written by
-- a metrics-only observer that runs ALONGSIDE the healer (it never influences the
-- restart/backoff decisions), best-effort, so it can't affect self-healing.
--
--   MTTD = detected_at - failure_at   (detection latency; failure_at best-effort)
--   MTTR = recovered_at - detected_at (recovery time; null while still down)
--
-- K8s episodes are keyed per-app (namespace + app), because healing deletes the
-- pod and the Deployment recreates it under a new name — so recovery is "a
-- healthy pod for this app reappeared", not "the same pod came back".
CREATE TABLE heal_episodes (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    target_kind  TEXT        NOT NULL,                 -- 'pod' | 'container'
    target_name  TEXT        NOT NULL,                 -- app name (k8s) / app name (docker)
    scope        TEXT        NOT NULL DEFAULT '',      -- namespace (k8s) / stack (docker)
    reason       TEXT        NOT NULL DEFAULT '',
    failure_at   TIMESTAMPTZ,                          -- actual failure time, best-effort (nullable)
    detected_at  TIMESTAMPTZ NOT NULL,                 -- first unhealthy observation
    recovered_at TIMESTAMPTZ,                          -- back to healthy (null = still down / gave up)
    attempts     INTEGER     NOT NULL DEFAULT 0,
    gave_up      BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON heal_episodes (target_kind, detected_at DESC);
CREATE INDEX ON heal_episodes (recovered_at);

-- +goose Down

DROP TABLE heal_episodes;
