package export

import (
	"bytes"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/pkg/memory"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	ts1 := time.Date(2026, 1, 15, 20, 30, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 15, 20, 31, 0, 0, time.UTC)

	data := ExportData{
		CampaignID:  "curse_of_strahd",
		TenantID:    "acme",
		LicenseTier: "shared",
		NPCs: []npcstore.NPCDefinition{
			{
				ID:          "npc_bartok",
				CampaignID:  "curse_of_strahd",
				Name:        "Bartok the Innkeeper",
				Personality: "Gruff but kind-hearted.",
				Voice: npcstore.VoiceConfig{
					Provider: "elevenlabs",
					VoiceID:  "voice_123",
				},
			},
		},
		Entities: []memory.Entity{
			{
				ID:         "e1",
				Type:       "npc",
				Name:       "Bartok",
				Attributes: map[string]any{"occupation": "innkeeper"},
				CreatedAt:  ts1,
				UpdatedAt:  ts1,
			},
			{
				ID:        "e2",
				Type:      "location",
				Name:      "The Silver Stag Inn",
				CreatedAt: ts1,
				UpdatedAt: ts1,
			},
		},
		Relationships: []memory.Relationship{
			{
				SourceID: "e1",
				TargetID: "e2",
				RelType:  "owns",
				Provenance: memory.Provenance{
					SessionID:  "s1",
					Timestamp:  ts1,
					Confidence: 0.95,
					Source:     "stated",
				},
				CreatedAt: ts1,
			},
		},
		Sessions: map[string][]memory.TranscriptEntry{
			"session-1": {
				{SpeakerName: "Player One", Text: "Hello, Bartok!", Timestamp: ts1},
				{SpeakerName: "Bartok", Text: "Welcome to the Silver Stag!", Timestamp: ts2, NPCID: "npc_bartok"},
			},
		},
	}

	// Export.
	var buf bytes.Buffer
	if err := WriteTarGz(&buf, data); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatal("exported archive is empty")
	}

	// Import.
	imported, err := ReadTarGz(&buf)
	if err != nil {
		t.Fatalf("ReadTarGz: %v", err)
	}

	// Verify metadata.
	if imported.Metadata.CampaignID != "curse_of_strahd" {
		t.Errorf("CampaignID = %q, want %q", imported.Metadata.CampaignID, "curse_of_strahd")
	}
	if imported.Metadata.TenantID != "acme" {
		t.Errorf("TenantID = %q, want %q", imported.Metadata.TenantID, "acme")
	}
	if imported.Metadata.LicenseTier != "shared" {
		t.Errorf("LicenseTier = %q, want %q", imported.Metadata.LicenseTier, "shared")
	}
	if imported.Metadata.Version != archiveVersion {
		t.Errorf("Version = %d, want %d", imported.Metadata.Version, archiveVersion)
	}

	// Verify NPCs.
	if len(imported.NPCs) != 1 {
		t.Fatalf("NPCs count = %d, want 1", len(imported.NPCs))
	}
	if imported.NPCs[0].Name != "Bartok the Innkeeper" {
		t.Errorf("NPC Name = %q, want %q", imported.NPCs[0].Name, "Bartok the Innkeeper")
	}
	if imported.NPCs[0].Voice.Provider != "elevenlabs" {
		t.Errorf("NPC Voice.Provider = %q, want %q", imported.NPCs[0].Voice.Provider, "elevenlabs")
	}

	// Verify entities.
	if len(imported.Entities) != 2 {
		t.Fatalf("Entities count = %d, want 2", len(imported.Entities))
	}

	// Verify relationships.
	if len(imported.Relationships) != 1 {
		t.Fatalf("Relationships count = %d, want 1", len(imported.Relationships))
	}
	if imported.Relationships[0].RelType != "owns" {
		t.Errorf("RelType = %q, want %q", imported.Relationships[0].RelType, "owns")
	}
	if imported.Relationships[0].Provenance.Confidence != 0.95 {
		t.Errorf("Confidence = %g, want 0.95", imported.Relationships[0].Provenance.Confidence)
	}

	// Verify sessions.
	if len(imported.Sessions) != 1 {
		t.Fatalf("Sessions count = %d, want 1", len(imported.Sessions))
	}

	// Find the session entries (key is the filename-derived session name).
	var sessionEntries []memory.TranscriptEntry
	for _, entries := range imported.Sessions {
		sessionEntries = entries
		break
	}
	if len(sessionEntries) != 2 {
		t.Fatalf("session entries count = %d, want 2", len(sessionEntries))
	}
	if sessionEntries[0].SpeakerName != "Player One" {
		t.Errorf("entry[0].SpeakerName = %q, want %q", sessionEntries[0].SpeakerName, "Player One")
	}
	if sessionEntries[1].Text != "Welcome to the Silver Stag!" {
		t.Errorf("entry[1].Text = %q, want %q", sessionEntries[1].Text, "Welcome to the Silver Stag!")
	}
}

func TestReadTarGz_MissingMetadata(t *testing.T) {
	t.Parallel()

	// Create an archive without metadata.json.
	data := ExportData{
		CampaignID:  "test",
		TenantID:    "acme",
		LicenseTier: "shared",
		Sessions:    map[string][]memory.TranscriptEntry{},
	}

	var buf bytes.Buffer
	if err := WriteTarGz(&buf, data); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	// This should succeed since WriteTarGz always writes metadata.
	imported, err := ReadTarGz(&buf)
	if err != nil {
		t.Fatalf("ReadTarGz: %v", err)
	}
	if imported.Metadata.CampaignID != "test" {
		t.Errorf("CampaignID = %q, want %q", imported.Metadata.CampaignID, "test")
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"Bartok the Innkeeper", "bartok_the_innkeeper"},
		{"Greymantle", "greymantle"},
		{"NPC/Evil", "npc_evil"},
		{"name.with.dots", "name_with_dots"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := sanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
