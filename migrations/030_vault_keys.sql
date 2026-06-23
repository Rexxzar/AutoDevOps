-- +goose Up

-- Envelope encryption for the secrets vault. Previously every row in `secrets`
-- was encrypted directly with the argon2id-derived key from MASTER_KEY (the KEK).
-- Now the KEK only *wraps* a random Data Encryption Key (DEK) stored here; the
-- DEK is what actually encrypts the `secrets` rows. This lets us:
--   * rotate the DEK (re-encrypt all secrets) and survive a restart with no env
--     change, because the new wrapped DEK is persisted here, and
--   * rotate the KEK (new MASTER_KEY) by re-wrapping just the tiny DEK.
-- Each rotation inserts a new row (version+1); old rows are kept inactive as an
-- audit trail. The bootstrap that creates version 1 and migrates legacy
-- KEK-encrypted secrets onto the DEK happens at server start in Go (it needs the
-- runtime KEK), not here.
CREATE TABLE vault_keys (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    version     INTEGER     NOT NULL UNIQUE,
    wrapped_dek BYTEA       NOT NULL,
    wrap_nonce  BYTEA       NOT NULL,
    active      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one active DEK at a time.
CREATE UNIQUE INDEX vault_keys_one_active ON vault_keys (active) WHERE active;

-- +goose Down

DROP TABLE vault_keys;
