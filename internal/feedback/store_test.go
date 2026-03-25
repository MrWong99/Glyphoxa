package feedback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/discord/commands"
)

func TestNewFileStore(t *testing.T) {
	t.Parallel()

	fs := NewFileStore("/tmp/test-feedback.jsonl")
	if fs == nil {
		t.Fatal("expected non-nil FileStore")
	}
	if fs.path != "/tmp/test-feedback.jsonl" {
		t.Errorf("path = %q, want %q", fs.path, "/tmp/test-feedback.jsonl")
	}
}

func TestSaveFeedback_CreatesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	fs := NewFileStore(path)

	fb := commands.Feedback{
		SessionID:      "sess-1",
		UserID:         "user-1",
		VoiceLatency:   4,
		NPCPersonality: 5,
		MemoryAccuracy: 3,
		DMWorkflow:     4,
		Comments:       "Great session!",
	}

	if err := fs.SaveFeedback("sess-1", fb); err != nil {
		t.Fatalf("SaveFeedback: %v", err)
	}

	// Verify the file exists and contains valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var record Record
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil { // -1 to strip newline
		t.Fatalf("Unmarshal: %v", err)
	}

	if record.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", record.SessionID, "sess-1")
	}
	if record.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", record.UserID, "user-1")
	}
	if record.VoiceLatency != 4 {
		t.Errorf("VoiceLatency = %d, want %d", record.VoiceLatency, 4)
	}
	if record.NPCPersonality != 5 {
		t.Errorf("NPCPersonality = %d, want %d", record.NPCPersonality, 5)
	}
	if record.MemoryAccuracy != 3 {
		t.Errorf("MemoryAccuracy = %d, want %d", record.MemoryAccuracy, 3)
	}
	if record.DMWorkflow != 4 {
		t.Errorf("DMWorkflow = %d, want %d", record.DMWorkflow, 4)
	}
	if record.Comments != "Great session!" {
		t.Errorf("Comments = %q, want %q", record.Comments, "Great session!")
	}
	if record.Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}

func TestSaveFeedback_AppendsMultiple(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	fs := NewFileStore(path)

	fb1 := commands.Feedback{SessionID: "sess-1", UserID: "user-1", VoiceLatency: 3}
	fb2 := commands.Feedback{SessionID: "sess-2", UserID: "user-2", VoiceLatency: 5}

	if err := fs.SaveFeedback("sess-1", fb1); err != nil {
		t.Fatalf("SaveFeedback #1: %v", err)
	}
	if err := fs.SaveFeedback("sess-2", fb2); err != nil {
		t.Fatalf("SaveFeedback #2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var r1, r2 Record
	if err := json.Unmarshal([]byte(lines[0]), &r1); err != nil {
		t.Fatalf("Unmarshal line 1: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &r2); err != nil {
		t.Fatalf("Unmarshal line 2: %v", err)
	}

	if r1.SessionID != "sess-1" {
		t.Errorf("line 1 SessionID = %q, want %q", r1.SessionID, "sess-1")
	}
	if r2.SessionID != "sess-2" {
		t.Errorf("line 2 SessionID = %q, want %q", r2.SessionID, "sess-2")
	}
}

func TestSaveFeedback_EmptyComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	fs := NewFileStore(path)

	fb := commands.Feedback{
		SessionID: "sess-1",
		UserID:    "user-1",
		Comments:  "",
	}

	if err := fs.SaveFeedback("sess-1", fb); err != nil {
		t.Fatalf("SaveFeedback: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Comments should be omitted (omitempty tag).
	if strings.Contains(string(data), `"comments"`) {
		t.Error("expected comments to be omitted for empty string")
	}
}

func TestSaveFeedback_InvalidPath(t *testing.T) {
	t.Parallel()

	fs := NewFileStore("/nonexistent/dir/feedback.jsonl")

	fb := commands.Feedback{SessionID: "sess-1"}
	err := fs.SaveFeedback("sess-1", fb)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestRecord_Fields(t *testing.T) {
	t.Parallel()

	r := Record{
		SessionID:      "s1",
		UserID:         "u1",
		VoiceLatency:   1,
		NPCPersonality: 2,
		MemoryAccuracy: 3,
		DMWorkflow:     4,
		Comments:       "test",
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var r2 Record
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if r2.SessionID != r.SessionID {
		t.Errorf("SessionID = %q, want %q", r2.SessionID, r.SessionID)
	}
	if r2.UserID != r.UserID {
		t.Errorf("UserID = %q, want %q", r2.UserID, r.UserID)
	}
	if r2.VoiceLatency != r.VoiceLatency {
		t.Errorf("VoiceLatency = %d, want %d", r2.VoiceLatency, r.VoiceLatency)
	}
}
