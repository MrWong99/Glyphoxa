package npcstore

import (
	"strings"
	"testing"
)

func TestNPCDefinition_Validate_Valid(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name:   "Greymantle",
		Engine: "cascaded",
		Voice: VoiceConfig{
			SpeedFactor: 1.0,
			PitchShift:  0,
		},
		BudgetTier: "fast",
	}

	if err := def.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
}

func TestNPCDefinition_Validate_EmptyName(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name: "",
	}

	err := def.Validate()
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name must not be empty") {
		t.Errorf("error should mention name, got: %v", err)
	}
}

func TestNPCDefinition_Validate_InvalidEngine(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name:   "Test",
		Engine: "invalid_engine",
	}

	err := def.Validate()
	if err == nil {
		t.Fatal("expected error for invalid engine")
	}
	if !strings.Contains(err.Error(), "engine") {
		t.Errorf("error should mention engine, got: %v", err)
	}
}

func TestNPCDefinition_Validate_InvalidBudgetTier(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name:       "Test",
		BudgetTier: "premium",
	}

	err := def.Validate()
	if err == nil {
		t.Fatal("expected error for invalid budget tier")
	}
	if !strings.Contains(err.Error(), "budget_tier") {
		t.Errorf("error should mention budget_tier, got: %v", err)
	}
}

func TestNPCDefinition_Validate_SpeedFactorOutOfRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		speedFactor float64
		wantErr     bool
	}{
		{"zero (default)", 0, false},
		{"lower bound", 0.5, false},
		{"upper bound", 2.0, false},
		{"normal", 1.0, false},
		{"too low", 0.3, true},
		{"too high", 2.5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			def := &NPCDefinition{
				Name:  "Test",
				Voice: VoiceConfig{SpeedFactor: tt.speedFactor},
			}
			err := def.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNPCDefinition_Validate_PitchShiftOutOfRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pitchShift float64
		wantErr    bool
	}{
		{"zero", 0, false},
		{"lower bound", -10, false},
		{"upper bound", 10, false},
		{"too low", -11, true},
		{"too high", 11, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			def := &NPCDefinition{
				Name:  "Test",
				Voice: VoiceConfig{PitchShift: tt.pitchShift},
			}
			err := def.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNPCDefinition_Validate_MultipleErrors(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name:       "",
		Engine:     "invalid",
		BudgetTier: "invalid",
		Voice: VoiceConfig{
			SpeedFactor: 5.0,
			PitchShift:  20,
		},
	}

	err := def.Validate()
	if err == nil {
		t.Fatal("expected multiple validation errors")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "name") {
		t.Error("error should mention name")
	}
	if !strings.Contains(errStr, "engine") {
		t.Error("error should mention engine")
	}
	if !strings.Contains(errStr, "budget_tier") {
		t.Error("error should mention budget_tier")
	}
	if !strings.Contains(errStr, "speed_factor") {
		t.Error("error should mention speed_factor")
	}
	if !strings.Contains(errStr, "pitch_shift") {
		t.Error("error should mention pitch_shift")
	}
}

func TestNPCDefinition_Validate_DefaultEngineAndTier(t *testing.T) {
	t.Parallel()

	// Empty engine and budget tier are valid (defaults applied at persistence).
	def := &NPCDefinition{
		Name: "Test NPC",
	}

	if err := def.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error for defaults: %v", err)
	}
}

func TestToIdentity_AllFields(t *testing.T) {
	t.Parallel()

	def := &NPCDefinition{
		Name:        "Greymantle",
		Personality: "A wise old sage",
		Voice: VoiceConfig{
			Provider:    "elevenlabs",
			VoiceID:     "voice-123",
			PitchShift:  2.5,
			SpeedFactor: 1.2,
		},
		KnowledgeScope:  []string{"arcane lore", "history"},
		SecretKnowledge: []string{"knows the location of the artifact"},
		BehaviorRules:   []string{"never lies"},
		GMHelper:        true,
		AddressOnly:     true,
	}

	identity := ToIdentity(def)

	if identity.Name != "Greymantle" {
		t.Errorf("Name = %q, want %q", identity.Name, "Greymantle")
	}
	if identity.Personality != "A wise old sage" {
		t.Errorf("Personality = %q, want %q", identity.Personality, "A wise old sage")
	}
	if identity.Voice.ID != "voice-123" {
		t.Errorf("Voice.ID = %q, want %q", identity.Voice.ID, "voice-123")
	}
	if identity.Voice.Provider != "elevenlabs" {
		t.Errorf("Voice.Provider = %q, want %q", identity.Voice.Provider, "elevenlabs")
	}
	if identity.Voice.PitchShift != 2.5 {
		t.Errorf("Voice.PitchShift = %g, want 2.5", identity.Voice.PitchShift)
	}
	if identity.Voice.SpeedFactor != 1.2 {
		t.Errorf("Voice.SpeedFactor = %g, want 1.2", identity.Voice.SpeedFactor)
	}
	if identity.Voice.Name != "Greymantle" {
		t.Errorf("Voice.Name = %q, want %q", identity.Voice.Name, "Greymantle")
	}
	if len(identity.KnowledgeScope) != 2 {
		t.Errorf("KnowledgeScope len = %d, want 2", len(identity.KnowledgeScope))
	}
	if len(identity.SecretKnowledge) != 1 {
		t.Errorf("SecretKnowledge len = %d, want 1", len(identity.SecretKnowledge))
	}
	if len(identity.BehaviorRules) != 1 {
		t.Errorf("BehaviorRules len = %d, want 1", len(identity.BehaviorRules))
	}
	if !identity.GMHelper {
		t.Error("GMHelper should be true")
	}
	if !identity.AddressOnly {
		t.Error("AddressOnly should be true")
	}
}

func TestDefaultEngine_Values(t *testing.T) {
	t.Parallel()

	if got := defaultEngine(""); got != "cascaded" {
		t.Errorf("defaultEngine('') = %q, want cascaded", got)
	}
	if got := defaultEngine("s2s"); got != "s2s" {
		t.Errorf("defaultEngine('s2s') = %q, want s2s", got)
	}
}

func TestDefaultBudgetTier_Values(t *testing.T) {
	t.Parallel()

	if got := defaultBudgetTier(""); got != "fast" {
		t.Errorf("defaultBudgetTier('') = %q, want fast", got)
	}
	if got := defaultBudgetTier("deep"); got != "deep" {
		t.Errorf("defaultBudgetTier('deep') = %q, want deep", got)
	}
}

func TestEmptySlice_NilAndNonNil(t *testing.T) {
	t.Parallel()

	got := emptySlice(nil)
	if got == nil {
		t.Error("emptySlice(nil) should return non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("emptySlice(nil) len = %d, want 0", len(got))
	}

	input := []string{"a", "b"}
	got = emptySlice(input)
	if len(got) != 2 {
		t.Errorf("emptySlice(non-nil) len = %d, want 2", len(got))
	}
}

func TestEmptyMap_NilAndNonNil(t *testing.T) {
	t.Parallel()

	got := emptyMap(nil)
	if got == nil {
		t.Error("emptyMap(nil) should return non-nil map")
	}
	if len(got) != 0 {
		t.Errorf("emptyMap(nil) len = %d, want 0", len(got))
	}

	input := map[string]any{"key": "val"}
	got = emptyMap(input)
	if len(got) != 1 {
		t.Errorf("emptyMap(non-nil) len = %d, want 1", len(got))
	}
}

func TestNPCDefinition_Validate_AllValidEngines(t *testing.T) {
	t.Parallel()

	engines := []string{"", "cascaded", "s2s", "sentence_cascade"}
	for _, eng := range engines {
		t.Run("engine_"+eng, func(t *testing.T) {
			t.Parallel()
			def := &NPCDefinition{Name: "Test", Engine: eng}
			if err := def.Validate(); err != nil {
				t.Errorf("Validate() error for engine %q: %v", eng, err)
			}
		})
	}
}

func TestNPCDefinition_Validate_AllValidBudgetTiers(t *testing.T) {
	t.Parallel()

	tiers := []string{"", "fast", "standard", "deep"}
	for _, tier := range tiers {
		t.Run("tier_"+tier, func(t *testing.T) {
			t.Parallel()
			def := &NPCDefinition{Name: "Test", BudgetTier: tier}
			if err := def.Validate(); err != nil {
				t.Errorf("Validate() error for tier %q: %v", tier, err)
			}
		})
	}
}
