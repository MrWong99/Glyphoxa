-- +goose Up
-- Session hygiene (#643, the schema half of #636). Since #636 the storage layer
-- writes the SHA-256 digest (base64url) of the session cookie secret into
-- sessions.token, never the secret itself — but the column was still called
-- `token`, and nothing ever deleted an expired row, so the table (with its
-- ip/ua audit columns) grew forever.
--
-- 1. Drop every expired row. That is also what makes a rewrite step
--    unnecessary: a row written BEFORE #636 holds a plaintext secret, a session
--    lives 30 days (auth.defaultSessionTTL), and #636 shipped on 2026-09-02 — so
--    on any deployment that has run a post-#636 build for a month, every
--    plaintext row is expired and goes here. (A plaintext and a digest value
--    are both 43 base64url characters, so the two cannot be told apart and
--    rehashed in place anyway.) A deployment jumping straight from a pre-#636
--    build may keep unexpired plaintext rows through this migration; they are
--    inert — every lookup hashes the presented cookie first, so a plaintext row
--    can never match, and its owner simply signs in again — and they age out
--    through Store.PurgeExpiredSessions like any other row.
DELETE FROM sessions WHERE expires_at <= now();

-- 2. Name the column for what it holds. The UNIQUE constraint (and its index)
--    is carried over unchanged, renamed to match.
ALTER TABLE sessions RENAME COLUMN token TO token_hash;
ALTER TABLE sessions RENAME CONSTRAINT sessions_token_key TO sessions_token_hash_key;

COMMENT ON COLUMN sessions.token_hash IS
    'SHA-256 (base64url, unpadded) of the glyphoxa_session cookie secret; the secret itself is never stored (ADR-0016, #636).';

-- +goose Down
-- Restores the column NAME only. The deleted expired rows are not recreated
-- (they were unusable), and the values stay digests: plaintext secrets were
-- never stored by a post-#636 build and cannot be recovered from a hash. A
-- post-#636 build reads the renamed column exactly as before.
COMMENT ON COLUMN sessions.token_hash IS NULL;
ALTER TABLE sessions RENAME CONSTRAINT sessions_token_hash_key TO sessions_token_key;
ALTER TABLE sessions RENAME COLUMN token_hash TO token;
