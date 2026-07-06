package rpc

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestApplyVoiceSelection_RoundTripsThroughPipelineReader pins the #224 drift:
// a voice persisted by the editor MUST read back as a tts.Voice with that
// VoiceID through the very decoder the voice pipeline uses (storage.VoiceFromJSON).
// The old marshalVoice wrote {"voice_id":…}, which that decoder read as empty —
// the silent NPC. This round-trip is the regression guard.
func TestApplyVoiceSelection_RoundTripsThroughPipelineReader(t *testing.T) {
	t.Parallel()
	blob := applyVoiceSelection(nil, "v123", "de")
	v, err := storage.VoiceFromJSON(blob)
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.VoiceID != "v123" {
		t.Errorf("round-tripped VoiceID = %q, want v123 (the #224 drift)", v.VoiceID)
	}
}

// TestApplyVoiceSelection_FirstSaveFillsDefaults: a first save (nil existing)
// fills the documented ElevenLabs defaults — provider, the campaign language, and
// the eleven_v3 / pcm_48000 Settings — not a bare voice id.
func TestApplyVoiceSelection_FirstSaveFillsDefaults(t *testing.T) {
	t.Parallel()
	v, err := storage.VoiceFromJSON(applyVoiceSelection(nil, "v123", "de"))
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.ProviderID != ttseleven.ProviderID {
		t.Errorf("ProviderID = %q, want %q", v.ProviderID, ttseleven.ProviderID)
	}
	if v.Language != "de" {
		t.Errorf("Language = %q, want the campaign language de", v.Language)
	}
	var s ttseleven.Settings
	if err := json.Unmarshal(v.Settings, &s); err != nil {
		t.Fatalf("Settings not valid ElevenLabs Settings: %v", err)
	}
	if s.OutputFormat != ttseleven.DefaultVoiceOutputFormat {
		t.Errorf("output_format = %q, want %q", s.OutputFormat, ttseleven.DefaultVoiceOutputFormat)
	}
	if s.ModelID != ttseleven.ModelV3 {
		t.Errorf("model_id = %q, want %q", s.ModelID, ttseleven.ModelV3)
	}
}

// TestApplyVoiceSelection_SameIDIsUnchanged: re-saving the editor with the same
// voice id leaves the persisted bytes exactly as they were — no default clobbers
// a tuned Settings blob (AC: "preserves existing ProviderID/Language/Settings").
func TestApplyVoiceSelection_SameIDIsUnchanged(t *testing.T) {
	t.Parallel()
	existing := existingVoiceBlob(t, "v1", "en", "Custom")
	got := applyVoiceSelection(existing, "v1", "de")
	if !bytes.Equal(got, existing) {
		t.Errorf("same-id re-save changed the bytes:\n got %s\nwant %s", got, existing)
	}
}

// TestApplyVoiceSelection_ChangedIDPreservesTuning: switching to a different
// voice id keeps the existing ProviderID/Language/Settings, swaps the VoiceID,
// and resets the display Name (it belonged to the old voice). The campaign
// language argument does NOT override the persisted Language on a change.
func TestApplyVoiceSelection_ChangedIDPreservesTuning(t *testing.T) {
	t.Parallel()
	existing := existingVoiceBlob(t, "v1", "en", "Custom")
	v, err := storage.VoiceFromJSON(applyVoiceSelection(existing, "v2", "fr"))
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.VoiceID != "v2" {
		t.Errorf("VoiceID = %q, want the new v2", v.VoiceID)
	}
	if v.Name != "" {
		t.Errorf("Name = %q, want reset to empty after a voice change", v.Name)
	}
	if v.ProviderID != ttseleven.ProviderID {
		t.Errorf("ProviderID = %q, want preserved %q", v.ProviderID, ttseleven.ProviderID)
	}
	if v.Language != "en" {
		t.Errorf("Language = %q, want the preserved en (not the fr argument)", v.Language)
	}
	prev, _ := storage.VoiceFromJSON(existing)
	if !bytes.Equal(v.Settings, prev.Settings) {
		t.Errorf("Settings not preserved across a voice change:\n got %s\nwant %s", v.Settings, prev.Settings)
	}
}

// TestApplyVoiceSelection_OldDriftSelfHeals: an existing {"voice_id":…} drift row
// (unreadable by the pipeline) is treated as a first save on the next edit, so a
// silent UI-configured NPC self-heals when re-saved — no re-pick needed.
func TestApplyVoiceSelection_OldDriftSelfHeals(t *testing.T) {
	t.Parallel()
	v, err := storage.VoiceFromJSON(applyVoiceSelection(json.RawMessage(`{"voice_id":"old"}`), "v123", "de"))
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.VoiceID != "v123" || v.ProviderID != ttseleven.ProviderID || v.Language != "de" {
		t.Errorf("old-drift edit did not self-heal to defaults: %+v", v)
	}
	if len(v.Settings) == 0 {
		t.Error("old-drift edit left Settings empty; the NPC would fall back to the 24 kHz default")
	}
}

// TestApplyVoiceSelection_ClearWritesEmptyObject: clearing the voice (empty id)
// writes the {} the schema defaults to, so "no voice" round-trips cleanly.
func TestApplyVoiceSelection_ClearWritesEmptyObject(t *testing.T) {
	t.Parallel()
	if got := applyVoiceSelection(existingVoiceBlob(t, "v1", "en", "Custom"), "", "de"); string(got) != `{}` {
		t.Errorf("clear wrote %s, want {}", got)
	}
}

// TestUnmarshalVoice_ReadsCanonicalShape: the RPC read path extracts the VoiceID
// from the canonical (seed / pipeline) shape — so a voiced seed NPC no longer
// shows "Pick a voice…" in the editor. The old drift shape and a bare {} read
// back as empty.
func TestUnmarshalVoice_ReadsCanonicalShape(t *testing.T) {
	t.Parallel()
	if got := unmarshalVoice(existingVoiceBlob(t, "seedvoice", "en", "Bart")); got != "seedvoice" {
		t.Errorf("canonical read = %q, want seedvoice", got)
	}
	if got := unmarshalVoice([]byte(`{"voice_id":"old"}`)); got != "" {
		t.Errorf("old-drift read = %q, want empty", got)
	}
	if got := unmarshalVoice([]byte(`{}`)); got != "" {
		t.Errorf("empty-object read = %q, want empty", got)
	}
}

// existingVoiceBlob builds a canonical persisted voice blob with a display name,
// as an already-saved editor row would hold.
func existingVoiceBlob(t *testing.T, voiceID, language, name string) json.RawMessage {
	t.Helper()
	v := ttseleven.DefaultVoice(voiceID, language)
	v.Name = name
	b, err := storage.VoiceToJSON(v)
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	return b
}
