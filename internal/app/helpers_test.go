// White-box tests for unexported helper functions in the app package:
// toInt, toFloat64, vadConfigFromProvider, sttConfigFromProvider,
// configBudgetTier, ttsFormatFromConfig, identityFromConfig.
package app

import (
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

func TestToInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		wantVal  int
		wantBool bool
	}{
		{"int value", 42, 42, true},
		{"int zero", 0, 0, true},
		{"negative int", -7, -7, true},
		{"float64 value", 3.14, 3, true},
		{"float64 whole", 100.0, 100, true},
		{"float64 zero", 0.0, 0, true},
		{"int64 value", int64(999), 999, true},
		{"int64 zero", int64(0), 0, true},
		{"string returns false", "hello", 0, false},
		{"bool returns false", true, 0, false},
		{"nil returns false", nil, 0, false},
		{"uint returns false", uint(5), 0, false},
		{"float32 returns false", float32(1.5), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := toInt(tt.input)
			if ok != tt.wantBool {
				t.Errorf("toInt(%v) ok = %v, want %v", tt.input, ok, tt.wantBool)
			}
			if got != tt.wantVal {
				t.Errorf("toInt(%v) = %d, want %d", tt.input, got, tt.wantVal)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		wantVal  float64
		wantBool bool
	}{
		{"float64 value", 3.14, 3.14, true},
		{"float64 zero", 0.0, 0.0, true},
		{"negative float64", -2.5, -2.5, true},
		{"int value", 42, 42.0, true},
		{"int zero", 0, 0.0, true},
		{"int64 value", int64(999), 999.0, true},
		{"int64 zero", int64(0), 0.0, true},
		{"string returns false", "hello", 0, false},
		{"bool returns false", true, 0, false},
		{"nil returns false", nil, 0, false},
		{"uint returns false", uint(5), 0, false},
		{"float32 returns false", float32(1.5), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := toFloat64(tt.input)
			if ok != tt.wantBool {
				t.Errorf("toFloat64(%v) ok = %v, want %v", tt.input, ok, tt.wantBool)
			}
			if got != tt.wantVal {
				t.Errorf("toFloat64(%v) = %f, want %f", tt.input, got, tt.wantVal)
			}
		})
	}
}

func TestVADConfigFromProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		entry config.ProviderEntry
		want  vad.Config
	}{
		{
			name:  "nil options returns defaults",
			entry: config.ProviderEntry{Name: "silero"},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "empty options returns defaults",
			entry: config.ProviderEntry{
				Name:    "silero",
				Options: map[string]any{},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "custom frame_size_ms as int",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms": 64,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      64,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "custom frame_size_ms as float64",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms": 20.0,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      20,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "custom thresholds",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"speech_threshold":  0.7,
					"silence_threshold": 0.3,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.7,
				SilenceThreshold: 0.3,
			},
		},
		{
			name: "all custom values",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms":     10,
					"speech_threshold":  0.8,
					"silence_threshold": 0.1,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      10,
				SpeechThreshold:  0.8,
				SilenceThreshold: 0.1,
			},
		},
		{
			name: "invalid frame_size_ms type ignored",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms": "thirty-two",
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "zero frame_size_ms ignored",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms": 0,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "negative frame_size_ms ignored",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"frame_size_ms": -10,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.25,
			},
		},
		{
			name: "thresholds from int values",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"speech_threshold":  1,
					"silence_threshold": 0,
				},
			},
			want: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  1.0,
				SilenceThreshold: 0.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := vadConfigFromProvider(tt.entry)
			if got != tt.want {
				t.Errorf("vadConfigFromProvider() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSTTConfigFromProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry config.ProviderEntry
		want  stt.StreamConfig
	}{
		{
			name:  "nil options returns defaults",
			entry: config.ProviderEntry{Name: "deepgram"},
			want: stt.StreamConfig{
				SampleRate: 16000,
				Channels:   1,
			},
		},
		{
			name: "empty options returns defaults",
			entry: config.ProviderEntry{
				Name:    "deepgram",
				Options: map[string]any{},
			},
			want: stt.StreamConfig{
				SampleRate: 16000,
				Channels:   1,
			},
		},
		{
			name: "language option set",
			entry: config.ProviderEntry{
				Name: "deepgram",
				Options: map[string]any{
					"language": "de-DE",
				},
			},
			want: stt.StreamConfig{
				SampleRate: 16000,
				Channels:   1,
				Language:   "de-DE",
			},
		},
		{
			name: "language option as non-string ignored",
			entry: config.ProviderEntry{
				Name: "deepgram",
				Options: map[string]any{
					"language": 42,
				},
			},
			want: stt.StreamConfig{
				SampleRate: 16000,
				Channels:   1,
			},
		},
		{
			name: "unrelated options ignored",
			entry: config.ProviderEntry{
				Name: "deepgram",
				Options: map[string]any{
					"model":       "nova-2",
					"punctuation": true,
				},
			},
			want: stt.StreamConfig{
				SampleRate: 16000,
				Channels:   1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sttConfigFromProvider(tt.entry)
			if got.SampleRate != tt.want.SampleRate {
				t.Errorf("SampleRate = %d, want %d", got.SampleRate, tt.want.SampleRate)
			}
			if got.Channels != tt.want.Channels {
				t.Errorf("Channels = %d, want %d", got.Channels, tt.want.Channels)
			}
			if got.Language != tt.want.Language {
				t.Errorf("Language = %q, want %q", got.Language, tt.want.Language)
			}
		})
	}
}

func TestConfigBudgetTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier config.BudgetTier
		want mcp.BudgetTier
	}{
		{"fast", config.BudgetTierFast, mcp.BudgetFast},
		{"standard", config.BudgetTierStandard, mcp.BudgetStandard},
		{"deep", config.BudgetTierDeep, mcp.BudgetDeep},
		{"empty string defaults to fast", "", mcp.BudgetFast},
		{"unknown string defaults to fast", "turbo", mcp.BudgetFast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := configBudgetTier(tt.tier)
			if got != tt.want {
				t.Errorf("configBudgetTier(%q) = %v, want %v", tt.tier, got, tt.want)
			}
		})
	}
}

func TestTTSFormatFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		entry          config.ProviderEntry
		wantSampleRate int
		wantChannels   int
	}{
		{
			name:           "elevenlabs default",
			entry:          config.ProviderEntry{Name: "elevenlabs"},
			wantSampleRate: 16000,
			wantChannels:   1,
		},
		{
			name:           "coqui default",
			entry:          config.ProviderEntry{Name: "coqui"},
			wantSampleRate: 22050,
			wantChannels:   1,
		},
		{
			name:           "unknown provider default",
			entry:          config.ProviderEntry{Name: "mystery-tts"},
			wantSampleRate: 22050,
			wantChannels:   1,
		},
		{
			name: "explicit pcm_24000 output_format",
			entry: config.ProviderEntry{
				Name: "elevenlabs",
				Options: map[string]any{
					"output_format": "pcm_24000",
				},
			},
			wantSampleRate: 24000,
			wantChannels:   1,
		},
		{
			name: "explicit pcm_16000 output_format",
			entry: config.ProviderEntry{
				Name: "openai",
				Options: map[string]any{
					"output_format": "pcm_16000",
				},
			},
			wantSampleRate: 16000,
			wantChannels:   1,
		},
		{
			name: "explicit pcm_44100 output_format",
			entry: config.ProviderEntry{
				Name: "custom",
				Options: map[string]any{
					"output_format": "pcm_44100",
				},
			},
			wantSampleRate: 44100,
			wantChannels:   1,
		},
		{
			name: "non-pcm format falls back to provider default",
			entry: config.ProviderEntry{
				Name: "elevenlabs",
				Options: map[string]any{
					"output_format": "mp3_44100",
				},
			},
			wantSampleRate: 16000,
			wantChannels:   1,
		},
		{
			name: "non-string output_format falls back to provider default",
			entry: config.ProviderEntry{
				Name: "coqui",
				Options: map[string]any{
					"output_format": 44100,
				},
			},
			wantSampleRate: 22050,
			wantChannels:   1,
		},
		{
			name: "empty options map falls back to provider default",
			entry: config.ProviderEntry{
				Name:    "elevenlabs",
				Options: map[string]any{},
			},
			wantSampleRate: 16000,
			wantChannels:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sr, ch := ttsFormatFromConfig(tt.entry)
			if sr != tt.wantSampleRate {
				t.Errorf("sampleRate = %d, want %d", sr, tt.wantSampleRate)
			}
			if ch != tt.wantChannels {
				t.Errorf("channels = %d, want %d", ch, tt.wantChannels)
			}
		})
	}
}

func TestIdentityFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		npc  config.NPCConfig
	}{
		{
			name: "basic NPC",
			npc: config.NPCConfig{
				Name:        "Grimjaw",
				Personality: "A gruff dwarven bartender.",
				Voice: config.VoiceConfig{
					Provider:    "elevenlabs",
					VoiceID:     "dwarf-1",
					PitchShift:  -2.0,
					SpeedFactor: 0.9,
				},
				KnowledgeScope: []string{"tavern", "local gossip"},
			},
		},
		{
			name: "GM helper NPC",
			npc: config.NPCConfig{
				Name:        "Narrator",
				Personality: "An omniscient narrator.",
				Voice: config.VoiceConfig{
					Provider: "openai",
					VoiceID:  "alloy",
				},
				GMHelper:    true,
				AddressOnly: true,
			},
		},
		{
			name: "minimal NPC",
			npc:  config.NPCConfig{Name: "Nobody"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := identityFromConfig(tt.npc)

			if id.Name != tt.npc.Name {
				t.Errorf("Name = %q, want %q", id.Name, tt.npc.Name)
			}
			if id.Personality != tt.npc.Personality {
				t.Errorf("Personality = %q, want %q", id.Personality, tt.npc.Personality)
			}
			if id.Voice.ID != tt.npc.Voice.VoiceID {
				t.Errorf("Voice.ID = %q, want %q", id.Voice.ID, tt.npc.Voice.VoiceID)
			}
			if id.Voice.Provider != tt.npc.Voice.Provider {
				t.Errorf("Voice.Provider = %q, want %q", id.Voice.Provider, tt.npc.Voice.Provider)
			}
			if id.Voice.PitchShift != tt.npc.Voice.PitchShift {
				t.Errorf("Voice.PitchShift = %f, want %f", id.Voice.PitchShift, tt.npc.Voice.PitchShift)
			}
			if id.Voice.SpeedFactor != tt.npc.Voice.SpeedFactor {
				t.Errorf("Voice.SpeedFactor = %f, want %f", id.Voice.SpeedFactor, tt.npc.Voice.SpeedFactor)
			}
			if id.GMHelper != tt.npc.GMHelper {
				t.Errorf("GMHelper = %v, want %v", id.GMHelper, tt.npc.GMHelper)
			}
			if id.AddressOnly != tt.npc.AddressOnly {
				t.Errorf("AddressOnly = %v, want %v", id.AddressOnly, tt.npc.AddressOnly)
			}
			if len(id.KnowledgeScope) != len(tt.npc.KnowledgeScope) {
				t.Errorf("KnowledgeScope len = %d, want %d", len(id.KnowledgeScope), len(tt.npc.KnowledgeScope))
			}
			for i, s := range tt.npc.KnowledgeScope {
				if i < len(id.KnowledgeScope) && id.KnowledgeScope[i] != s {
					t.Errorf("KnowledgeScope[%d] = %q, want %q", i, id.KnowledgeScope[i], s)
				}
			}
		})
	}
}
