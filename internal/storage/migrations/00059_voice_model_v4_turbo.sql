-- +goose Up

-- Move ElevenLabs agent voices off eleven_v3 onto eleven_v4_turbo (#702,
-- ADR-0023 amendment 2026-11-22). eleven_v3 was the adapter default until now
-- and every persisted voice records its model explicitly (the first UI save, the
-- seed and 00016 all wrote "model_id":"eleven_v3"), so changing the Go default
-- alone moves no existing NPC. v4 Turbo speaks the same audio-tag vocabulary, its
-- first audio lands ~135 ms after the request over the same HTTP /stream path
-- (~600 ms on v3) and it bills half v3's credits per character. No UI ever let a
-- GM choose a model, so an eleven_v3 pin is the old default, not a choice.
--
-- agents.voice is the canonical Go-field shape
-- ({"ProviderID","VoiceID","Name","Language","Settings":{model_id,…}}); only the
-- Settings.model_id key of an ElevenLabs voice pinned to eleven_v3 is rewritten,
-- every other key is kept. Voices on any other model, non-ElevenLabs voices and
-- {} no-voice rows are untouched, so re-running is a no-op.
UPDATE agents
   SET voice = jsonb_set(voice, '{Settings,model_id}', '"eleven_v4_turbo"'::jsonb)
 WHERE voice->>'ProviderID' = 'elevenlabs'
   AND voice->'Settings'->>'model_id' = 'eleven_v3';

-- +goose Down

-- Move eleven_v4_turbo ElevenLabs voices back to eleven_v3, the default of the
-- code a rollback returns to. Which rows Up rewrote is not recorded, so a voice
-- saved on eleven_v4_turbo after Up moves back too; both models speak the same
-- tag vocabulary, so no NPC is broken by it.
UPDATE agents
   SET voice = jsonb_set(voice, '{Settings,model_id}', '"eleven_v3"'::jsonb)
 WHERE voice->>'ProviderID' = 'elevenlabs'
   AND voice->'Settings'->>'model_id' = 'eleven_v4_turbo';
