package voice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice/mock"
)

const (
	testGuild   = snowflake.ID(1001)
	testChannel = snowflake.ID(2002)
)

func TestManagerOpenGetClose(t *testing.T) {
	fm := mock.NewManager()
	m := newTestManager(fm)

	sess, err := m.Open(context.Background(), testGuild, testChannel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if sess.State() != Ready {
		t.Fatalf("state got %v want Ready", sess.State())
	}

	got, ok := m.Get(testGuild)
	if !ok || got != sess {
		t.Fatalf("Get returned (%v, %v) want the opened session", got, ok)
	}
	if _, ok := m.Get(snowflake.ID(9999)); ok {
		t.Fatal("Get on unknown guild should report false")
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sess.State() != Closed {
		t.Fatalf("session state after Manager.Close got %v want Closed", sess.State())
	}
	if _, ok := m.Get(testGuild); ok {
		t.Fatal("Get should report false after Close")
	}
	if removed := fm.Removed(); len(removed) != 1 || removed[0] != testGuild {
		t.Fatalf("RemoveConn calls got %v want [%d]", removed, testGuild)
	}
}

func TestManagerWithDaveWarnsWhenUnavailable(t *testing.T) {
	// In the default (stub) build DaveAvailable() is false, so a Manager that
	// expects DAVE (the default) must warn rather than connect silently
	// unencrypted. Under -tags dave this build has real DAVE and stays quiet.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	newTestManager(mock.NewManager(), WithLogger(logger)) // WithDave defaults true
	warned := strings.Contains(buf.String(), "DAVE expected but unavailable")
	if warned == DaveAvailable() {
		t.Fatalf("warn=%v but DaveAvailable=%v; they must be opposite", warned, DaveAvailable())
	}

	// Opting out of DAVE never warns, regardless of build.
	buf.Reset()
	newTestManager(mock.NewManager(), WithLogger(logger), WithDave(false))
	if strings.Contains(buf.String(), "DAVE") {
		t.Fatalf("WithDave(false) should not warn, got: %q", buf.String())
	}
}

func TestManagerOpenFailureUnwindsConn(t *testing.T) {
	fm := mock.NewManager()
	fm.OpenErr = errors.New("gateway refused")
	m := newTestManager(fm)

	if _, err := m.Open(context.Background(), testGuild, testChannel); err == nil {
		t.Fatal("expected Open to fail")
	}
	if _, ok := m.Get(testGuild); ok {
		t.Fatal("failed Open must not register a session")
	}
	// The conn disgo created on our behalf must be removed so it does not leak.
	if removed := fm.Removed(); len(removed) != 1 || removed[0] != testGuild {
		t.Fatalf("RemoveConn calls got %v want [%d]", removed, testGuild)
	}
}

func TestManagerOpenReplacesExisting(t *testing.T) {
	fm := mock.NewManager()
	m := newTestManager(fm)

	first, err := m.Open(context.Background(), testGuild, testChannel)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := m.Open(context.Background(), testGuild, testChannel)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if first == second {
		t.Fatal("re-Open should produce a fresh session")
	}
	if first.State() != Closed {
		t.Fatalf("replaced session state got %v want Closed", first.State())
	}
	got, ok := m.Get(testGuild)
	if !ok || got != second {
		t.Fatal("Get should return the replacement session")
	}
}

// TestManagerOpenConcurrentSameGuild_NoLeak pins the per-guild Open
// serialization: racing Opens for one guild must never leave a Session alive
// outside the map (its Inbound consumer would block forever). Exactly one
// winner survives in the Manager; every other Session ends Closed.
func TestManagerOpenConcurrentSameGuild_NoLeak(t *testing.T) {
	fm := mock.NewManager()
	m := newTestManager(fm)

	const n = 8
	sessions := make([]*Session, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := m.Open(context.Background(), testGuild, testChannel)
			if err != nil {
				t.Errorf("Open %d: %v", i, err)
				return
			}
			sessions[i] = s
		}(i)
	}
	wg.Wait()

	winner, ok := m.Get(testGuild)
	if !ok {
		t.Fatal("no session left in the manager")
	}
	for i, s := range sessions {
		if s == nil || s == winner {
			continue
		}
		if s.State() != Closed {
			t.Errorf("session %d neither won nor was closed — leaked (state %v)", i, s.State())
		}
	}
	if winner.State() == Closed {
		t.Error("the winning session is closed")
	}
}
