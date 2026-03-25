package commands

import (
	"errors"
	"testing"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

func TestParseRating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"valid 1", "1", 1},
		{"valid 3", "3", 3},
		{"valid 5", "5", 5},
		{"above max clamps to 5", "9", 5},
		{"zero clamps to 1", "0", 1},
		{"negative clamps to 1", "-3", 1},
		{"non-numeric defaults to 1", "abc", 1},
		{"empty string defaults to 1", "", 1},
		{"whitespace around valid", "  3  ", 3},
		{"whitespace around invalid", "  abc  ", 1},
		{"large number clamps to 5", "100", 5},
		{"boundary 6 clamps to 5", "6", 5},
		{"valid 2", "2", 2},
		{"valid 4", "4", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseRating(tt.input)
			if got != tt.want {
				t.Errorf("parseRating(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// mockFeedbackStore is a simple mock for FeedbackStore.
type mockFeedbackStore struct {
	saved    []Feedback
	saveErr  error
}

func (m *mockFeedbackStore) SaveFeedback(_ string, fb Feedback) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, fb)
	return nil
}

func TestNewFeedbackCommands(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	store := &mockFeedbackStore{}
	sessionID := "session-123"
	getSessionID := func() string { return sessionID }

	fc := NewFeedbackCommands(perms, store, getSessionID)

	if fc == nil {
		t.Fatal("NewFeedbackCommands returned nil")
	}
	if fc.perms != perms {
		t.Error("perms not set correctly")
	}
	if fc.store != store {
		t.Error("store not set correctly")
	}
	if fc.getSessionID() != sessionID {
		t.Errorf("getSessionID() = %q, want %q", fc.getSessionID(), sessionID)
	}
}

func TestNewFeedbackCommands_NilStore(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	fc := NewFeedbackCommands(perms, nil, func() string { return "" })

	if fc == nil {
		t.Fatal("NewFeedbackCommands returned nil with nil store")
	}
	if fc.store != nil {
		t.Error("store should be nil")
	}
}

func TestFeedbackDefinition(t *testing.T) {
	t.Parallel()

	fc := NewFeedbackCommands(
		discordbot.NewPermissionChecker(""),
		nil,
		func() string { return "" },
	)
	def := fc.Definition()

	if def.Name != "feedback" {
		t.Errorf("Name = %q, want %q", def.Name, "feedback")
	}
	if def.Description != "Submit post-session feedback" {
		t.Errorf("Description = %q, want %q", def.Description, "Submit post-session feedback")
	}
	if len(def.Options) != 0 {
		t.Errorf("Options count = %d, want 0 (feedback opens a modal)", len(def.Options))
	}
}

func TestFeedbackRegister(t *testing.T) {
	t.Parallel()

	fc := NewFeedbackCommands(
		discordbot.NewPermissionChecker(""),
		nil,
		func() string { return "" },
	)
	router := discordbot.NewCommandRouter()
	fc.Register(router)

	cmds := router.ApplicationCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.CommandName() == "feedback" {
			found = true
			break
		}
	}
	if !found {
		t.Error("feedback command not registered with router")
	}
}

func TestFeedbackStruct(t *testing.T) {
	t.Parallel()

	fb := Feedback{
		SessionID:      "sess-abc",
		UserID:         "user-123",
		VoiceLatency:   4,
		NPCPersonality: 5,
		MemoryAccuracy: 3,
		DMWorkflow:     2,
		Comments:       "Great session!",
	}

	if fb.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", fb.SessionID, "sess-abc")
	}
	if fb.UserID != "user-123" {
		t.Errorf("UserID = %q, want %q", fb.UserID, "user-123")
	}
	if fb.VoiceLatency != 4 {
		t.Errorf("VoiceLatency = %d, want %d", fb.VoiceLatency, 4)
	}
	if fb.NPCPersonality != 5 {
		t.Errorf("NPCPersonality = %d, want %d", fb.NPCPersonality, 5)
	}
	if fb.MemoryAccuracy != 3 {
		t.Errorf("MemoryAccuracy = %d, want %d", fb.MemoryAccuracy, 3)
	}
	if fb.DMWorkflow != 2 {
		t.Errorf("DMWorkflow = %d, want %d", fb.DMWorkflow, 2)
	}
	if fb.Comments != "Great session!" {
		t.Errorf("Comments = %q, want %q", fb.Comments, "Great session!")
	}
}

func TestMockFeedbackStore_SaveFeedback(t *testing.T) {
	t.Parallel()

	store := &mockFeedbackStore{}
	fb := Feedback{
		SessionID:      "sess-1",
		UserID:         "user-1",
		VoiceLatency:   3,
		NPCPersonality: 4,
		MemoryAccuracy: 5,
		DMWorkflow:     2,
		Comments:       "test",
	}

	if err := store.SaveFeedback("sess-1", fb); err != nil {
		t.Fatalf("SaveFeedback error: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved count = %d, want 1", len(store.saved))
	}
	if store.saved[0].SessionID != "sess-1" {
		t.Errorf("saved SessionID = %q, want %q", store.saved[0].SessionID, "sess-1")
	}
}

func TestMockFeedbackStore_SaveFeedbackError(t *testing.T) {
	t.Parallel()

	store := &mockFeedbackStore{saveErr: errors.New("db down")}
	fb := Feedback{SessionID: "sess-1"}

	err := store.SaveFeedback("sess-1", fb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "db down" {
		t.Errorf("error = %q, want %q", err.Error(), "db down")
	}
}

func TestNewFeedbackCommands_SessionIDFunc(t *testing.T) {
	t.Parallel()

	callCount := 0
	getSessionID := func() string {
		callCount++
		return "dynamic-session"
	}

	fc := NewFeedbackCommands(
		discordbot.NewPermissionChecker(""),
		nil,
		getSessionID,
	)

	// Call multiple times to verify the function is properly wired.
	id1 := fc.getSessionID()
	id2 := fc.getSessionID()

	if id1 != "dynamic-session" {
		t.Errorf("first call = %q, want %q", id1, "dynamic-session")
	}
	if id2 != "dynamic-session" {
		t.Errorf("second call = %q, want %q", id2, "dynamic-session")
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2", callCount)
	}
}
