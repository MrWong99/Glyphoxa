-- +goose Up
-- Session hygiene (#643, the schema half of #636). Nothing ever deleted an
-- expired session row, so the table — with the IP address and user agent
-- captured at each login — grew forever. Drop every expired row now;
-- Store.PurgeExpiredSessions keeps it that way (at boot and daily).
--
-- This also retires the plaintext rows written before #636: a session lives 30
-- days (auth.defaultSessionTTL) and #636 shipped on 2026-09-02, so on any
-- deployment that has run a post-#636 build for a month every such row is
-- expired and goes here. (A plaintext secret and its digest are both 43
-- base64url characters, so the two cannot be told apart and rehashed in place.)
-- A deployment jumping straight from a pre-#636 build may keep unexpired
-- plaintext rows; they are inert — every lookup hashes the presented cookie
-- first, so a plaintext row can never match — and they age out like any other.
DELETE FROM sessions WHERE expires_at <= now();

-- The column keeps its name. #643 proposed renaming it to token_hash; that was
-- deliberately NOT done: a rename makes the release non-rollbackable (an older
-- binary would fail every authenticated request until someone ran `migrate
-- down` with the newer image) for a purely cosmetic gain. The comment says what
-- the name does not.
COMMENT ON COLUMN sessions.token IS
    'SHA-256 digest (base64url, unpadded) of the glyphoxa_session cookie secret — NOT the secret itself, which is never stored (ADR-0016, #636).';

-- +goose Down
-- The deleted rows were expired and unusable; they are not recreated.
COMMENT ON COLUMN sessions.token IS NULL;
