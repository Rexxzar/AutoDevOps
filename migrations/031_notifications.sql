-- +goose Up

-- User-facing notifications surfaced in the dashboard bell. A curated, read/unread
-- view of notable control-plane events (deploy success/failure, self-heal give-up,
-- resource apply/failure), persisted so they survive reloads. Single-tenant, so
-- no user_id. Real-time delivery is the existing SSE hub (a "notification" event);
-- this table is just persistence — no broker, consistent with the locked stack.
CREATE TABLE notifications (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    type       TEXT        NOT NULL,                       -- deploy | heal | resource | compliance
    severity   TEXT        NOT NULL DEFAULT 'info',        -- info | success | warning | critical
    title      TEXT        NOT NULL,
    body       TEXT        NOT NULL DEFAULT '',
    link       TEXT        NOT NULL DEFAULT '',            -- in-app deep link, e.g. /deployments/<id>
    read       BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON notifications (read, created_at DESC);
CREATE INDEX ON notifications (created_at DESC);

-- +goose Down

DROP TABLE notifications;
