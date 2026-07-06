-- +goose Up

-- Repair agents.voice rows written by the pre-#224 web editor as
-- {"voice_id":"…"} — a shape the voice pipeline cannot read. tts.Voice carries no
-- JSON tags (ADR-0022 keeps it untouched), so Go's unmarshal (case-insensitive
-- but NOT underscore-insensitive) never maps voice_id onto VoiceID: the NPC
-- hydrates with an empty VoiceID and is silent at synthesis time.
--
-- Rewrite each such row into the canonical Go-field shape
-- ({"ProviderID","VoiceID","Name","Language","Settings"}) the seed rows already
-- hold and hydration already reads, filling the documented ElevenLabs defaults
-- (eleven_v3 / pcm_48000, matching elevenlabs.DefaultVoice) and the OWNING
-- campaign's language. Name is left empty (the old shape carried none). Only rows
-- that actually carry a non-empty voice_id AND lack the canonical VoiceID key are
-- touched, so already-canonical rows and {} no-voice rows are left alone and the
-- migration is safe to re-run. This is a one-way repair (see Down).
UPDATE agents a
SET voice = jsonb_build_object(
    'ProviderID', 'elevenlabs',
    'VoiceID', a.voice->>'voice_id',
    'Name', '',
    'Language', c.language,
    'Settings', '{"model_id":"eleven_v3","output_format":"pcm_48000","voice_settings":{"stability":0.5,"similarity_boost":0.75,"use_speaker_boost":true}}'::jsonb)
FROM campaign c
WHERE c.id = a.campaign_id
  AND a.voice ? 'voice_id'
  AND a.voice->>'voice_id' <> ''
  AND NOT a.voice ? 'VoiceID';

-- +goose Down

-- Intentional no-op. This is a one-way data repair: the pre-migration
-- {"voice_id":…} shape was a defect (unreadable by the pipeline, #224), so
-- reversing the rewrite would deliberately re-break every repaired row. There is
-- nothing safe to undo, so Down does nothing.
SELECT 1;
