package wirenpc

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestNPCVoice_DelegatesToDefaultVoice pins the seed-equivalence contract after
// the #224 refactor: npcVoice() is elevenlabs.DefaultVoice(voiceID, "en") with
// the display Name added, so the seed source and the RPC first-save default can
// never diverge. Every field except the ones npcVoice specializes (VoiceID and
// Name) must equal DefaultVoice's.
func TestNPCVoice_DelegatesToDefaultVoice(t *testing.T) {
	got := npcVoice()
	base := ttseleven.DefaultVoice(elevenGeorgeVoiceID, "en")

	if got.ProviderID != base.ProviderID {
		t.Errorf("ProviderID = %q, want %q", got.ProviderID, base.ProviderID)
	}
	if got.Language != base.Language {
		t.Errorf("Language = %q, want %q", got.Language, base.Language)
	}
	if !bytes.Equal(got.Settings, base.Settings) {
		t.Errorf("Settings = %q, want %q", got.Settings, base.Settings)
	}
	if got.VoiceID != elevenGeorgeVoiceID {
		t.Errorf("VoiceID = %q, want %q", got.VoiceID, elevenGeorgeVoiceID)
	}
	if got.Name != "Bart" {
		t.Errorf("Name = %q, want Bart", got.Name)
	}
}

// TestNPCSpecFromAgent_DelegatesToVoiceFromJSON: the hydration path reads the
// canonical shape through the shared storage.VoiceFromJSON mapper. A canonical
// blob populates the Voice; the old {"voice_id":…} drift decodes to an empty
// VoiceID (the #224 silent-NPC condition), same as the mapper's own contract.
func TestNPCSpecFromAgent_DelegatesToVoiceFromJSON(t *testing.T) {
	canonical := ttseleven.DefaultVoice("v123", "de")
	blob, err := storage.VoiceToJSON(canonical)
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	spec, err := npcSpecFromAgent(storage.Agent{ID: uuid.New(), Name: "Ana", Voice: blob})
	if err != nil {
		t.Fatalf("npcSpecFromAgent: %v", err)
	}
	if spec.voice.VoiceID != "v123" {
		t.Errorf("canonical VoiceID = %q, want v123", spec.voice.VoiceID)
	}

	drift, err := npcSpecFromAgent(storage.Agent{ID: uuid.New(), Name: "Bob", Voice: json.RawMessage(`{"voice_id":"v9"}`)})
	if err != nil {
		t.Fatalf("npcSpecFromAgent(drift): %v", err)
	}
	if drift.voice.VoiceID != "" {
		t.Errorf("old-drift VoiceID = %q, want empty (the pre-fix silent condition)", drift.voice.VoiceID)
	}
}

// TestLogVoiceGaps_ErrorsOnEmptyVoiceID is AC6: a loaded NPC hydrating with an
// empty VoiceID must produce an ERROR at session start naming the NPC — instead
// of failing silently per-turn at synthesis time. A voiced NPC produces nothing.
func TestLogVoiceGaps_ErrorsOnEmptyVoiceID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	voiced := npcSpec{agentID: uuid.NewString(), name: "Voiced", voice: npcVoice()}
	silent := npcSpec{agentID: uuid.NewString(), name: "Silent"} // zero Voice → empty VoiceID

	logVoiceGaps(log, []npcSpec{voiced, silent})

	out := buf.String()
	if strings.Count(out, "level=ERROR") != 1 {
		t.Errorf("want exactly one ERROR line, got:\n%s", out)
	}
	if !strings.Contains(out, "empty VoiceID") {
		t.Errorf("ERROR must name the empty-VoiceID condition, got:\n%s", out)
	}
	if !strings.Contains(out, "Silent") {
		t.Errorf("ERROR must name the silent NPC, got:\n%s", out)
	}
	if strings.Contains(out, "Voiced") {
		t.Errorf("voiced NPC must not be flagged, got:\n%s", out)
	}
}
