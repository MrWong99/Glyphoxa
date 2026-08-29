-- +goose Up
-- Session-Highlights detector tuning, per Campaign (#632 follow-up): the score
-- bar (0-10) a classify window must reach and the consecutive at-or-above-bar
-- windows that confirm a moment into a clip. The #632 diagnosis showed a
-- low-stakes campaign (system=test, language=de) peaking at score 3 against the
-- fixed engine bar of 8.0 — these columns let the GM tune that per campaign.
--
-- 0 means "use the engine default" (8.0 / 2): internal/highlight
-- Config.withDefaults treats any non-positive value as unset, so a zero column
-- flows through unchanged and campaigns keep tracking future default changes
-- until a GM explicitly overrides. NOT NULL DEFAULT 0 migrates existing rows
-- without a backfill (the 00023 pattern).
-- The RPC layer is the range authority; the CHECKs are defense-in-depth against
-- writers that bypass it (psql, a future path without validation). Postgres
-- sorts NaN above every number, so BETWEEN also excludes NaN — which
-- withDefaults would NOT neutralize (NaN <= 0 is false), leaving a silently
-- dead detector.
ALTER TABLE campaign ADD COLUMN highlight_bar double precision NOT NULL DEFAULT 0
    CHECK (highlight_bar BETWEEN 0 AND 10);
ALTER TABLE campaign ADD COLUMN highlight_confirm_windows integer NOT NULL DEFAULT 0
    CHECK (highlight_confirm_windows BETWEEN 0 AND 10);

-- +goose Down
ALTER TABLE campaign DROP COLUMN IF EXISTS highlight_confirm_windows;
ALTER TABLE campaign DROP COLUMN IF EXISTS highlight_bar;
