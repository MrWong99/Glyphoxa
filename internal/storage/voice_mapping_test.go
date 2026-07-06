package storage_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// TestVoiceFromJSON_EmptyIsZeroVoice: an empty column or a bare {} (the schema
// default, and the "no voice" the editor writes) decodes to the zero Voice with
// no error — "no voice selected" round-trips cleanly, exactly like the empty
// GrantSet the tool-grant mapper produces for a grantless agent (#215).
func TestVoiceFromJSON_EmptyIsZeroVoice(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "{}"} {
		v, err := storage.VoiceFromJSON(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("VoiceFromJSON(%q) err = %v, want nil", raw, err)
		}
		if !reflect.DeepEqual(v, tts.Voice{}) {
			t.Errorf("VoiceFromJSON(%q) = %+v, want zero Voice", raw, v)
		}
	}
}

// TestVoiceFromJSON_Canonical reads the canonical Go-field shape — the shape the
// seed rows already hold and the voice pipeline already hydrates — into a fully
// populated tts.Voice.
func TestVoiceFromJSON_Canonical(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"ProviderID":"elevenlabs","VoiceID":"v123","Name":"Bart","Language":"de","Settings":{"model_id":"eleven_v3"}}`)
	v, err := storage.VoiceFromJSON(raw)
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.ProviderID != "elevenlabs" || v.VoiceID != "v123" || v.Name != "Bart" || v.Language != "de" {
		t.Errorf("identity mismatch: %+v", v)
	}
	if string(v.Settings) != `{"model_id":"eleven_v3"}` {
		t.Errorf("Settings = %q, want the opaque blob passed through verbatim", v.Settings)
	}
}

// TestVoiceFromJSON_NullSettingsNormalized: a Voice persisted with a null
// Settings must decode with Settings == nil (not the literal json.RawMessage
// "null"), so a settings-less Voice round-trips identically. This normalization
// used to live in wirenpc.npcSpecFromAgent; it now lives with the canonical
// mapper so every reader shares it.
func TestVoiceFromJSON_NullSettingsNormalized(t *testing.T) {
	t.Parallel()
	v, err := storage.VoiceFromJSON(json.RawMessage(`{"VoiceID":"x","Settings":null}`))
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.Settings != nil {
		t.Errorf("Settings = %q, want nil after null normalization", v.Settings)
	}
}

// TestVoiceFromJSON_Unparsable surfaces a genuinely corrupt blob as an error
// rather than a silent zero Voice.
func TestVoiceFromJSON_Unparsable(t *testing.T) {
	t.Parallel()
	if _, err := storage.VoiceFromJSON(json.RawMessage(`not json`)); err == nil {
		t.Error("VoiceFromJSON(garbage) err = nil, want a parse error")
	}
}

// TestVoiceFromJSON_OldDriftReadsEmpty pins the #224 root cause: the pre-fix web
// writer's {"voice_id":…} shape decodes to an EMPTY VoiceID under the canonical
// reader — Go's json unmarshal is case-insensitive but NOT underscore-insensitive,
// so voice_id never maps to VoiceID. This empty VoiceID is exactly what made
// UI-saved NPCs silent at synthesis time.
func TestVoiceFromJSON_OldDriftReadsEmpty(t *testing.T) {
	t.Parallel()
	v, err := storage.VoiceFromJSON(json.RawMessage(`{"voice_id":"v123"}`))
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if v.VoiceID != "" {
		t.Errorf("old-drift VoiceID = %q, want empty (documents why the NPC was silent)", v.VoiceID)
	}
}

// TestVoiceToJSON_RoundTrip: VoiceToJSON then VoiceFromJSON reproduces the Voice
// — the writer and reader share ONE canonical shape and cannot drift.
func TestVoiceToJSON_RoundTrip(t *testing.T) {
	t.Parallel()
	in := tts.Voice{
		ProviderID: "elevenlabs",
		VoiceID:    "v1",
		Name:       "N",
		Language:   "en",
		Settings:   json.RawMessage(`{"model_id":"eleven_v3"}`),
	}
	b, err := storage.VoiceToJSON(in)
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	out, err := storage.VoiceFromJSON(b)
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}
