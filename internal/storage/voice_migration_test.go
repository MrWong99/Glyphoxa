//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestMigration00016_RepairsVoiceDrift is the #224 data-repair bar: rows written
// by the pre-fix web editor as {"voice_id":…} — unreadable by the voice pipeline
// — are rewritten in-place to the canonical tts.Voice shape so a previously
// silent UI-configured NPC is audible after migration + a session restart, with
// no re-edit. A {} (no-voice) row is left untouched. Mirrors the 00013 backfill
// coverage pattern (UpTo to the pre-migration version, seed, then Up).
func TestMigration00016_RepairsVoiceDrift(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}

	// Apply through 00015 only — the drift-repair migration does not exist yet, so
	// the {"voice_id":…} shape can be inserted as the old writer would have.
	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatalf("migrate up to 00015: %v", err)
	}

	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Voice Repair Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// A 'de' campaign — the migration must stamp the OWNING campaign's language.
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Repair Campaign", System: "dnd5e", Language: "de",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	driftID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Drift",
		Voice: []byte(`{"voice_id":"abc"}`),
	})
	if err != nil {
		t.Fatalf("CreateAgent(drift): %v", err)
	}
	emptyID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "NoVoice",
		Voice: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateAgent(empty): %v", err)
	}

	// Apply 00016: the drift-repair migration.
	if _, err := provider.UpTo(ctx, 16); err != nil {
		t.Fatalf("migrate up to 00016: %v", err)
	}

	// The drift row is now the canonical shape the pipeline reads.
	drift, err := st.GetAgent(ctx, driftID)
	if err != nil {
		t.Fatalf("GetAgent(drift): %v", err)
	}
	v, err := storage.VoiceFromJSON(drift.Voice)
	if err != nil {
		t.Fatalf("VoiceFromJSON(drift): %v", err)
	}
	if v.VoiceID != "abc" {
		t.Errorf("repaired VoiceID = %q, want abc (the old voice_id value)", v.VoiceID)
	}
	if v.ProviderID != ttseleven.ProviderID {
		t.Errorf("repaired ProviderID = %q, want %q", v.ProviderID, ttseleven.ProviderID)
	}
	if v.Language != "de" {
		t.Errorf("repaired Language = %q, want the campaign's de", v.Language)
	}
	var s ttseleven.Settings
	if err := json.Unmarshal(v.Settings, &s); err != nil {
		t.Fatalf("repaired Settings not valid ElevenLabs Settings: %v", err)
	}
	if s.OutputFormat != ttseleven.DefaultVoiceOutputFormat {
		t.Errorf("repaired output_format = %q, want %q", s.OutputFormat, ttseleven.DefaultVoiceOutputFormat)
	}
	if s.ModelID != ttseleven.ModelV3 {
		t.Errorf("repaired model_id = %q, want %q", s.ModelID, ttseleven.ModelV3)
	}

	// The {} no-voice row is untouched.
	empty, err := st.GetAgent(ctx, emptyID)
	if err != nil {
		t.Fatalf("GetAgent(empty): %v", err)
	}
	if got := string(empty.Voice); got != "{}" {
		t.Errorf("no-voice row = %q, want it left as {}", got)
	}
}
