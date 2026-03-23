package session

import (
	"context"
	"fmt"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

func TestWorkerHandler_StartStop(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)
	ctx := context.Background()

	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	statuses, err := handler.GetStatus(ctx)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != gateway.SessionActive {
		t.Errorf("got state %v, want %v", statuses[0].State, gateway.SessionActive)
	}

	if err := handler.StopSession(ctx, "s1"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	statuses, _ = handler.GetStatus(ctx)
	if len(statuses) != 0 {
		t.Fatalf("got %d statuses after stop, want 0", len(statuses))
	}
}

func TestWorkerHandler_DuplicateStart(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)
	ctx := context.Background()

	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer handler.StopAll(ctx)

	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s1"}); err == nil {
		t.Fatal("expected error for duplicate session")
	}
}

func TestWorkerHandler_StopNotFound(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)

	if err := handler.StopSession(context.Background(), "nonexistent"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestWorkerHandler_FactoryError(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, _ gateway.StartSessionRequest) (*Runtime, error) {
			return nil, fmt.Errorf("factory error")
		},
		nil,
	)

	err := handler.StartSession(context.Background(), gateway.StartSessionRequest{SessionID: "s1"})
	if err == nil {
		t.Fatal("expected error from factory")
	}
}

func TestWorkerHandler_ActiveSessionIDs(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)
	ctx := context.Background()

	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("start s1: %v", err)
	}
	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s2"}); err != nil {
		t.Fatalf("start s2: %v", err)
	}
	defer handler.StopAll(ctx)

	ids := handler.ActiveSessionIDs()
	if len(ids) != 2 {
		t.Fatalf("got %d active sessions, want 2", len(ids))
	}
}

func TestWorkerHandler_StartSession_FactoryContextOutlivesRPC(t *testing.T) {
	t.Parallel()

	var factoryCtx context.Context
	handler := NewWorkerHandler(
		func(ctx context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			factoryCtx = ctx
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)

	// Simulate a gRPC RPC context that gets canceled after the handler returns.
	rpcCtx, rpcCancel := context.WithCancel(context.Background())

	if err := handler.StartSession(rpcCtx, gateway.StartSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer handler.StopAll(context.Background())

	// Cancel the RPC context (simulates gRPC canceling after handler returns).
	rpcCancel()

	// The factory context must NOT be canceled — it's tied to the session
	// lifecycle, not the RPC. Before the fix, the RPC context was passed
	// directly to the factory, so the audio pipeline and consolidator
	// would die as soon as the StartSession RPC completed.
	if factoryCtx.Err() != nil {
		t.Fatalf("factory context was canceled: %v — pipeline and consolidator would die", factoryCtx.Err())
	}
}

func TestWorkerHandler_StopAll(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)
	ctx := context.Background()

	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("start s1: %v", err)
	}
	if err := handler.StartSession(ctx, gateway.StartSessionRequest{SessionID: "s2"}); err != nil {
		t.Fatalf("start s2: %v", err)
	}

	handler.StopAll(ctx)

	statuses, _ := handler.GetStatus(ctx)
	if len(statuses) != 0 {
		t.Fatalf("got %d statuses after StopAll, want 0", len(statuses))
	}
}
