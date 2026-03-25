package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
)

func TestMemoryGuard_WriteEntry(t *testing.T) {
	t.Run("successful write", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		mg := NewMemoryGuard(store)

		entry := memory.TranscriptEntry{Text: "hello"}
		err := mg.WriteEntry(context.Background(), "s1", entry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded after successful write")
		}
		if store.CallCount("WriteEntry") != 1 {
			t.Errorf("expected 1 WriteEntry call, got %d", store.CallCount("WriteEntry"))
		}
	})

	t.Run("write failure is swallowed", func(t *testing.T) {
		store := &memorymock.SessionStore{
			WriteEntryErr: errors.New("disk full"),
		}
		mg := NewMemoryGuard(store)

		entry := memory.TranscriptEntry{Text: "hello"}
		err := mg.WriteEntry(context.Background(), "s1", entry)
		if err != nil {
			t.Fatalf("expected nil error (swallowed), got %v", err)
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed write")
		}
	})

	t.Run("recovers from degraded after successful write", func(t *testing.T) {
		store := &memorymock.SessionStore{
			WriteEntryErr: errors.New("temporary failure"),
		}
		mg := NewMemoryGuard(store)

		// First call fails.
		_ = mg.WriteEntry(context.Background(), "s1", memory.TranscriptEntry{Text: "a"})
		if !mg.IsDegraded() {
			t.Error("should be degraded")
		}

		// Fix the store.
		store.WriteEntryErr = nil

		// Second call succeeds.
		_ = mg.WriteEntry(context.Background(), "s1", memory.TranscriptEntry{Text: "b"})
		if mg.IsDegraded() {
			t.Error("should have recovered from degraded state")
		}
	})
}

func TestMemoryGuard_GetRecent(t *testing.T) {
	t.Run("successful read", func(t *testing.T) {
		entries := []memory.TranscriptEntry{
			{Text: "hello"},
			{Text: "world"},
		}
		store := &memorymock.SessionStore{
			GetRecentResult: entries,
		}
		mg := NewMemoryGuard(store)

		got, err := mg.GetRecent(context.Background(), "s1", 5*time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 entries, got %d", len(got))
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded")
		}
	})

	t.Run("read failure returns empty slice", func(t *testing.T) {
		store := &memorymock.SessionStore{
			GetRecentErr: errors.New("connection refused"),
		}
		mg := NewMemoryGuard(store)

		got, err := mg.GetRecent(context.Background(), "s1", 5*time.Minute)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %d entries", len(got))
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed read")
		}
	})
}

func TestMemoryGuard_Search(t *testing.T) {
	t.Run("successful search", func(t *testing.T) {
		entries := []memory.TranscriptEntry{
			{Text: "found it"},
		}
		store := &memorymock.SessionStore{
			SearchResult: entries,
		}
		mg := NewMemoryGuard(store)

		got, err := mg.Search(context.Background(), "goblin", memory.SearchOpts{SessionID: "s1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 result, got %d", len(got))
		}
	})

	t.Run("search failure returns empty slice", func(t *testing.T) {
		store := &memorymock.SessionStore{
			SearchErr: errors.New("index corrupted"),
		}
		mg := NewMemoryGuard(store)

		got, err := mg.Search(context.Background(), "dragon", memory.SearchOpts{})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %d results", len(got))
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed search")
		}
	})
}

func TestMemoryGuard_IsDegraded(t *testing.T) {
	t.Run("initially not degraded", func(t *testing.T) {
		mg := NewMemoryGuard(&memorymock.SessionStore{})
		if mg.IsDegraded() {
			t.Error("should not be degraded initially")
		}
	})

	t.Run("mixed operations track degraded state", func(t *testing.T) {
		store := &memorymock.SessionStore{}
		mg := NewMemoryGuard(store)

		// Successful write — not degraded.
		_ = mg.WriteEntry(context.Background(), "s1", memory.TranscriptEntry{})
		if mg.IsDegraded() {
			t.Error("should not be degraded after success")
		}

		// Failed search — degraded.
		store.SearchErr = errors.New("oops")
		_, _ = mg.Search(context.Background(), "q", memory.SearchOpts{})
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed search")
		}

		// Successful write recovers.
		store.SearchErr = nil
		_ = mg.WriteEntry(context.Background(), "s1", memory.TranscriptEntry{})
		if mg.IsDegraded() {
			t.Error("should have recovered after successful write")
		}
	})
}

func TestMemoryGuard_EntryCount(t *testing.T) {
	t.Parallel()

	t.Run("successful count", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{EntryCountResult: 42}
		mg := NewMemoryGuard(store)

		got, err := mg.EntryCount(context.Background(), "s1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 42 {
			t.Errorf("expected 42 entries, got %d", got)
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded after successful count")
		}
		if store.CallCount("EntryCount") != 1 {
			t.Errorf("expected 1 EntryCount call, got %d", store.CallCount("EntryCount"))
		}
	})

	t.Run("count failure returns 0", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{
			EntryCountErr: errors.New("db error"),
		}
		mg := NewMemoryGuard(store)

		got, err := mg.EntryCount(context.Background(), "s1")
		if err != nil {
			t.Fatalf("expected nil error (swallowed), got %v", err)
		}
		if got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed count")
		}
	})
}

func TestMemoryGuard_ListSessions(t *testing.T) {
	t.Parallel()

	t.Run("successful list", func(t *testing.T) {
		t.Parallel()

		sessions := []memory.SessionInfo{
			{SessionID: "s1"},
			{SessionID: "s2"},
		}
		store := &memorymock.SessionStore{ListSessionsResult: sessions}
		mg := NewMemoryGuard(store)

		got, err := mg.ListSessions(context.Background(), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 sessions, got %d", len(got))
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded after successful list")
		}
	})

	t.Run("list failure returns empty slice", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{
			ListSessionsErr: errors.New("connection lost"),
		}
		mg := NewMemoryGuard(store)

		got, err := mg.ListSessions(context.Background(), 10)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %d sessions", len(got))
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed list")
		}
	})
}

func TestMemoryGuard_StartSession(t *testing.T) {
	t.Parallel()

	t.Run("successful start", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		mg := NewMemoryGuard(store)

		err := mg.StartSession(context.Background(), "s1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded after successful start")
		}
		if store.CallCount("StartSession") != 1 {
			t.Errorf("expected 1 StartSession call, got %d", store.CallCount("StartSession"))
		}
	})

	t.Run("start failure is swallowed", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{
			StartSessionErr: errors.New("table locked"),
		}
		mg := NewMemoryGuard(store)

		err := mg.StartSession(context.Background(), "s1")
		if err != nil {
			t.Fatalf("expected nil error (swallowed), got %v", err)
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed start")
		}
	})

	t.Run("recovers from degraded after success", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{
			StartSessionErr: errors.New("temporary"),
		}
		mg := NewMemoryGuard(store)

		_ = mg.StartSession(context.Background(), "s1")
		if !mg.IsDegraded() {
			t.Error("should be degraded")
		}

		store.StartSessionErr = nil
		_ = mg.StartSession(context.Background(), "s2")
		if mg.IsDegraded() {
			t.Error("should have recovered")
		}
	})
}

func TestMemoryGuard_EndSession(t *testing.T) {
	t.Parallel()

	t.Run("successful end", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{}
		mg := NewMemoryGuard(store)

		err := mg.EndSession(context.Background(), "s1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mg.IsDegraded() {
			t.Error("should not be degraded after successful end")
		}
		if store.CallCount("EndSession") != 1 {
			t.Errorf("expected 1 EndSession call, got %d", store.CallCount("EndSession"))
		}
	})

	t.Run("end failure is swallowed", func(t *testing.T) {
		t.Parallel()

		store := &memorymock.SessionStore{
			EndSessionErr: errors.New("db unavailable"),
		}
		mg := NewMemoryGuard(store)

		err := mg.EndSession(context.Background(), "s1")
		if err != nil {
			t.Fatalf("expected nil error (swallowed), got %v", err)
		}
		if !mg.IsDegraded() {
			t.Error("should be degraded after failed end")
		}
	})
}

func TestMemoryGuard_ImplementsSessionStore(t *testing.T) {
	// This is a compile-time check, but let's also verify at runtime.
	var _ memory.SessionStore = NewMemoryGuard(&memorymock.SessionStore{})
}
