package session

import (
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

func TestChunkEntries(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name       string
		sessionID  string
		entries    []memory.TranscriptEntry
		wantCount  int
		wantChecks func(t *testing.T, chunks []memory.Chunk)
	}{
		{
			name:      "single entry",
			sessionID: "session-1",
			entries: []memory.TranscriptEntry{
				{SpeakerID: "player1", SpeakerName: "Alice", Text: "Hello there", Timestamp: now},
			},
			wantCount: 1,
			wantChecks: func(t *testing.T, chunks []memory.Chunk) {
				t.Helper()
				c := chunks[0]
				if c.SessionID != "session-1" {
					t.Errorf("SessionID = %q, want %q", c.SessionID, "session-1")
				}
				if c.Content != "Hello there" {
					t.Errorf("Content = %q, want %q", c.Content, "Hello there")
				}
				if c.SpeakerID != "player1" {
					t.Errorf("SpeakerID = %q, want %q", c.SpeakerID, "player1")
				}
				if c.EntityID != "" {
					t.Errorf("EntityID = %q, want empty for player entry", c.EntityID)
				}
				if c.ID == "" {
					t.Error("ID should be a non-empty UUID")
				}
			},
		},
		{
			name:      "multiple entries",
			sessionID: "session-2",
			entries: []memory.TranscriptEntry{
				{SpeakerID: "player1", Text: "First", Timestamp: now},
				{SpeakerID: "npc-1", Text: "Second", NPCID: "npc-1", Timestamp: now.Add(time.Second)},
				{SpeakerID: "player1", Text: "Third", Timestamp: now.Add(2 * time.Second)},
			},
			wantCount: 3,
		},
		{
			name:      "empty text is skipped",
			sessionID: "session-3",
			entries: []memory.TranscriptEntry{
				{SpeakerID: "player1", Text: "Before", Timestamp: now},
				{SpeakerID: "player1", Text: "", Timestamp: now.Add(time.Second)},
				{SpeakerID: "player1", Text: "After", Timestamp: now.Add(2 * time.Second)},
			},
			wantCount: 2,
		},
		{
			name:      "NPC entry gets EntityID",
			sessionID: "session-4",
			entries: []memory.TranscriptEntry{
				{SpeakerID: "npc-0-Grimjaw", Text: "Welcome to my forge!", NPCID: "npc-0-Grimjaw", Timestamp: now},
			},
			wantCount: 1,
			wantChecks: func(t *testing.T, chunks []memory.Chunk) {
				t.Helper()
				if chunks[0].EntityID != "npc-0-Grimjaw" {
					t.Errorf("EntityID = %q, want %q", chunks[0].EntityID, "npc-0-Grimjaw")
				}
			},
		},
		{
			name:      "no entries",
			sessionID: "session-5",
			entries:   nil,
			wantCount: 0,
		},
		{
			name:      "all empty text",
			sessionID: "session-6",
			entries: []memory.TranscriptEntry{
				{SpeakerID: "player1", Text: ""},
				{SpeakerID: "player2", Text: ""},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chunks := ChunkEntries(tt.sessionID, tt.entries)
			if len(chunks) != tt.wantCount {
				t.Fatalf("len(chunks) = %d, want %d", len(chunks), tt.wantCount)
			}

			// Verify all chunks have unique IDs and no embedding.
			seen := make(map[string]struct{})
			for _, c := range chunks {
				if c.ID == "" {
					t.Error("chunk ID must not be empty")
				}
				if _, dup := seen[c.ID]; dup {
					t.Errorf("duplicate chunk ID: %s", c.ID)
				}
				seen[c.ID] = struct{}{}

				if c.Embedding != nil {
					t.Error("Embedding should be nil (caller embeds)")
				}
			}

			if tt.wantChecks != nil {
				tt.wantChecks(t, chunks)
			}
		})
	}
}
