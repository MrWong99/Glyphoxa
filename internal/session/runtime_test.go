package session

import (
	"context"
	"testing"
)

func TestRuntime_StartStop(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{
		SessionID: "test-session",
	})

	ctx := context.Background()

	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("start: %v", err)
	}

	if rt.SessionID() != "test-session" {
		t.Errorf("got session ID %q, want %q", rt.SessionID(), "test-session")
	}

	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestRuntime_DoubleStart(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})
	ctx := context.Background()

	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer func() { _ = rt.Stop(ctx) }()

	if err := rt.Start(ctx, nil); err == nil {
		t.Fatal("expected error on double start")
	}
}

func TestRuntime_StopNotRunning(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})
	ctx := context.Background()

	if err := rt.Stop(ctx); err == nil {
		t.Fatal("expected error on stop when not running")
	}
}

func TestRuntime_ClosersCalledInReverse(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(RuntimeConfig{SessionID: "test"})

	var order []int
	rt.AddCloser(func() error { order = append(order, 1); return nil })
	rt.AddCloser(func() error { order = append(order, 2); return nil })
	rt.AddCloser(func() error { order = append(order, 3); return nil })

	ctx := context.Background()
	if err := rt.Start(ctx, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("got %d closers called, want 3", len(order))
	}
	if order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Errorf("closers called in wrong order: %v, want [3 2 1]", order)
	}
}
