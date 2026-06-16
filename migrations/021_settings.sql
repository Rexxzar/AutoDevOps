-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

INSERT INTO settings (key, value) VALUES ('self_healing_scope', 'autodevops_only');

-- +goose Down
DROP TABLE settings;
