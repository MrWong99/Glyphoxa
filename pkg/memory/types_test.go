package memory_test

import (
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

func TestSpeakerRole_DisplaySuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role memory.SpeakerRole
		want string
	}{
		{memory.RoleDefault, ""},
		{memory.RoleGM, " [GM]"},
		{memory.RoleGMAssistant, " [GM Assistant]"},
		{memory.SpeakerRole("unknown"), ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			t.Parallel()
			got := tt.role.DisplaySuffix()
			if got != tt.want {
				t.Errorf("SpeakerRole(%q).DisplaySuffix() = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}

func TestTranscriptEntry_IsNPC(t *testing.T) {
	t.Parallel()

	t.Run("NPC entry", func(t *testing.T) {
		t.Parallel()
		e := memory.TranscriptEntry{NPCID: "npc-1"}
		if !e.IsNPC() {
			t.Error("expected IsNPC()=true for entry with NPCID")
		}
	})

	t.Run("player entry", func(t *testing.T) {
		t.Parallel()
		e := memory.TranscriptEntry{}
		if e.IsNPC() {
			t.Error("expected IsNPC()=false for entry without NPCID")
		}
	})
}
