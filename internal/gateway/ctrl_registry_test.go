package gateway

import (
	"context"
	"sync"
	"testing"
)

// stubSessionController is a minimal SessionController for registry tests.
type stubSessionController struct {
	id string
}

func (s *stubSessionController) Start(context.Context, SessionStartRequest) error { return nil }
func (s *stubSessionController) Stop(context.Context, string) error               { return nil }
func (s *stubSessionController) IsActive(string) bool                             { return false }
func (s *stubSessionController) Info(string) (SessionInfo, bool)                  { return SessionInfo{}, false }

func TestSessionControllerRegistry_RegisterLookup(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	ctrl := &stubSessionController{id: "ctrl-1"}

	reg.Register("tenant-a", ctrl)

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		got, ok := reg.Lookup("tenant-a")
		if !ok {
			t.Fatal("expected lookup to succeed")
		}
		sc, _ := got.(*stubSessionController)
		if sc.id != "ctrl-1" {
			t.Errorf("got id %q, want %q", sc.id, "ctrl-1")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, ok := reg.Lookup("tenant-nonexistent")
		if ok {
			t.Error("expected lookup to return false for missing tenant")
		}
	})
}

func TestSessionControllerRegistry_Remove(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	reg.Register("tenant-b", &stubSessionController{id: "ctrl-2"})

	// Confirm it exists.
	if _, ok := reg.Lookup("tenant-b"); !ok {
		t.Fatal("expected tenant-b to exist before removal")
	}

	reg.Remove("tenant-b")

	// Confirm it is gone.
	if _, ok := reg.Lookup("tenant-b"); ok {
		t.Error("expected tenant-b to be gone after removal")
	}
}

func TestSessionControllerRegistry_RemoveNonexistent(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	// Should not panic.
	reg.Remove("does-not-exist")
}

func TestSessionControllerRegistry_RegisterOverwrite(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	reg.Register("tenant-c", &stubSessionController{id: "first"})
	reg.Register("tenant-c", &stubSessionController{id: "second"})

	got, ok := reg.Lookup("tenant-c")
	if !ok {
		t.Fatal("expected lookup to succeed after overwrite")
	}
	sc, _ := got.(*stubSessionController)
	if sc.id != "second" {
		t.Errorf("got id %q, want %q after overwrite", sc.id, "second")
	}
}

func TestSessionControllerRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()

	var wg sync.WaitGroup
	const n = 50

	// Concurrent registrations.
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reg.Register(tenantID(idx), &stubSessionController{id: itoa(idx)})
		}(i)
	}

	// Concurrent lookups.
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reg.Lookup(tenantID(idx))
		}(i)
	}

	// Concurrent removals.
	for i := range n / 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reg.Remove(tenantID(idx))
		}(i)
	}

	wg.Wait()
}

func TestSessionControllerRegistry_ForEach(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	reg.Register("tenant-a", &stubSessionController{id: "a"})
	reg.Register("tenant-b", &stubSessionController{id: "b"})

	visited := make(map[string]string)
	reg.ForEach(func(tenantID string, ctrl SessionController) bool {
		sc := ctrl.(*stubSessionController)
		visited[tenantID] = sc.id
		return true
	})

	if len(visited) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(visited))
	}
	if visited["tenant-a"] != "a" || visited["tenant-b"] != "b" {
		t.Errorf("unexpected entries: %v", visited)
	}
}

func TestSessionControllerRegistry_ForEach_StopsOnFalse(t *testing.T) {
	t.Parallel()

	reg := NewSessionControllerRegistry()
	for i := range 5 {
		reg.Register(tenantID(i), &stubSessionController{id: itoa(i)})
	}

	count := 0
	reg.ForEach(func(_ string, _ SessionController) bool {
		count++
		return count < 2
	})

	if count != 2 {
		t.Errorf("expected ForEach to stop after 2 iterations, got %d", count)
	}
}
