-- +goose Up

-- System log store: every slog record the binary emits is tee'd here by a custom
-- slog.Handler (in addition to stdout), so the dashboard can show "everything
-- under the hood" with filters. Writes are async + best-effort (drop on overload)
-- so logging never blocks the server. `source` is derived from the message
-- prefix ("docker:", "k8s:", "deploy:" ...). Bounded by a periodic retention trim.
CREATE TABLE logs (
    id         BIGSERIAL   PRIMARY KEY,
    level      TEXT        NOT NULL,            -- DEBUG | INFO | WARN | ERROR
    source     TEXT        NOT NULL DEFAULT '', -- component, derived from message prefix
    message    TEXT        NOT NULL,
    attrs      JSONB       NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON logs (created_at DESC);
CREATE INDEX ON logs (level, created_at DESC);
CREATE INDEX ON logs (source, created_at DESC);

-- +goose Down

DROP TABLE logs;
