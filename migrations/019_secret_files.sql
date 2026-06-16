-- +goose Up

-- Secret files: per-app uploaded config/credential files (e.g. a Firebase
-- service-account JSON read at startup) mounted read-only into the
-- container at the given path. secret_files_id points at an encrypted
-- vault blob (JSON {containerPath: base64Content}, name
-- "secret-files:<deploymentID>:<appName>"); secret_file_paths mirrors the
-- map's keys in cleartext (paths aren't secret) so the dashboard can list
-- configured files without decrypting.
ALTER TABLE deployment_apps
    ADD COLUMN secret_files_id UUID REFERENCES secrets(id),
    ADD COLUMN secret_file_paths TEXT[];

-- +goose Down

ALTER TABLE deployment_apps
    DROP COLUMN secret_file_paths,
    DROP COLUMN secret_files_id;
