package commands

import (
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

func TestGMHelperVoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		npcs []config.NPCConfig
		want tts.VoiceProfile
	}{
		{
			name: "returns GM helper voice when present",
			npcs: []config.NPCConfig{
				{
					Name: "Regular",
					Voice: config.VoiceConfig{
						VoiceID:     "voice-regular",
						Provider:    "elevenlabs",
						PitchShift:  1.0,
						SpeedFactor: 1.0,
					},
				},
				{
					Name:     "Helper",
					GMHelper: true,
					Voice: config.VoiceConfig{
						VoiceID:     "voice-helper",
						Provider:    "google",
						PitchShift:  2.5,
						SpeedFactor: 1.2,
					},
				},
			},
			want: tts.VoiceProfile{
				ID:          "voice-helper",
				Provider:    "google",
				PitchShift:  2.5,
				SpeedFactor: 1.2,
			},
		},
		{
			name: "falls back to first NPC when no GM helper",
			npcs: []config.NPCConfig{
				{
					Name: "FirstNPC",
					Voice: config.VoiceConfig{
						VoiceID:     "voice-first",
						Provider:    "coqui",
						PitchShift:  -1.0,
						SpeedFactor: 0.9,
					},
				},
				{
					Name: "SecondNPC",
					Voice: config.VoiceConfig{
						VoiceID:     "voice-second",
						Provider:    "elevenlabs",
						PitchShift:  0,
						SpeedFactor: 1.0,
					},
				},
			},
			want: tts.VoiceProfile{
				ID:          "voice-first",
				Provider:    "coqui",
				PitchShift:  -1.0,
				SpeedFactor: 0.9,
			},
		},
		{
			name: "empty NPC list returns zero profile",
			npcs: nil,
			want: tts.VoiceProfile{},
		},
		{
			name: "GM helper is first in list",
			npcs: []config.NPCConfig{
				{
					Name:     "OnlyHelper",
					GMHelper: true,
					Voice: config.VoiceConfig{
						VoiceID:     "voice-only",
						Provider:    "elevenlabs",
						PitchShift:  0,
						SpeedFactor: 1.0,
					},
				},
			},
			want: tts.VoiceProfile{
				ID:          "voice-only",
				Provider:    "elevenlabs",
				PitchShift:  0,
				SpeedFactor: 1.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vrc := &VoiceRecapCommands{npcs: tt.npcs}
			got := vrc.gmHelperVoice()
			if got.ID != tt.want.ID {
				t.Errorf("ID = %q, want %q", got.ID, tt.want.ID)
			}
			if got.Provider != tt.want.Provider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.want.Provider)
			}
			if got.PitchShift != tt.want.PitchShift {
				t.Errorf("PitchShift = %v, want %v", got.PitchShift, tt.want.PitchShift)
			}
			if got.SpeedFactor != tt.want.SpeedFactor {
				t.Errorf("SpeedFactor = %v, want %v", got.SpeedFactor, tt.want.SpeedFactor)
			}
		})
	}
}

func TestGMHelperVoice_MultipleHelpers(t *testing.T) {
	t.Parallel()

	// When multiple NPCs are marked as GM helper, the first one wins.
	npcs := []config.NPCConfig{
		{
			Name:     "FirstHelper",
			GMHelper: true,
			Voice: config.VoiceConfig{
				VoiceID:  "voice-first-helper",
				Provider: "elevenlabs",
			},
		},
		{
			Name:     "SecondHelper",
			GMHelper: true,
			Voice: config.VoiceConfig{
				VoiceID:  "voice-second-helper",
				Provider: "google",
			},
		},
	}

	vrc := &VoiceRecapCommands{npcs: npcs}
	got := vrc.gmHelperVoice()

	if got.ID != "voice-first-helper" {
		t.Errorf("ID = %q, want %q (first GM helper should win)", got.ID, "voice-first-helper")
	}
}

func TestVoiceRecapCommands_Fields(t *testing.T) {
	t.Parallel()

	npcs := []config.NPCConfig{
		{Name: "Narrator", GMHelper: true},
	}
	vrc := &VoiceRecapCommands{
		npcs: npcs,
	}

	if len(vrc.npcs) != 1 {
		t.Errorf("npcs count = %d, want 1", len(vrc.npcs))
	}
	if vrc.npcs[0].Name != "Narrator" {
		t.Errorf("npcs[0].Name = %q, want %q", vrc.npcs[0].Name, "Narrator")
	}
}

func TestVoiceRecapCommands_NilGenerator(t *testing.T) {
	t.Parallel()

	vrc := &VoiceRecapCommands{
		generator: nil,
	}

	if vrc.generator != nil {
		t.Error("expected nil generator")
	}
}

func TestVoiceRecapCommands_NilRecapStore(t *testing.T) {
	t.Parallel()

	vrc := &VoiceRecapCommands{
		recapStore: nil,
	}

	if vrc.recapStore != nil {
		t.Error("expected nil recapStore")
	}
}

func TestVoiceRecapCommands_NilSessionStore(t *testing.T) {
	t.Parallel()

	vrc := &VoiceRecapCommands{
		sessionStore: nil,
	}

	if vrc.sessionStore != nil {
		t.Error("expected nil sessionStore")
	}
}

func TestVoiceRecapConfig_Fields(t *testing.T) {
	t.Parallel()

	npcs := []config.NPCConfig{
		{Name: "TestNPC"},
	}
	cfg := VoiceRecapConfig{
		NPCs: npcs,
	}

	if len(cfg.NPCs) != 1 {
		t.Errorf("NPCs count = %d, want 1", len(cfg.NPCs))
	}
	if cfg.NPCs[0].Name != "TestNPC" {
		t.Errorf("NPCs[0].Name = %q, want %q", cfg.NPCs[0].Name, "TestNPC")
	}
}
