package local_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/local"
)

func TestClient_StartSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		startFn local.SessionStartFunc
		wantErr bool
	}{
		{
			name:    "success",
			startFn: func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		},
		{
			name:    "start error",
			startFn: func(_ context.Context, _ gateway.StartSessionRequest) error { return fmt.Errorf("boom") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := local.NewClient(tt.startFn, func(_ context.Context, _ string) error { return nil })

			err := c.StartSession(context.Background(), gateway.StartSessionRequest{
				SessionID: "test-session",
				TenantID:  "local",
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("StartSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_StopSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stopFn  local.SessionStopFunc
		wantErr bool
	}{
		{
			name:   "success",
			stopFn: func(_ context.Context, _ string) error { return nil },
		},
		{
			name:   "stop error",
			stopFn: func(_ context.Context, _ string) error { return fmt.Errorf("boom") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := local.NewClient(
				func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
				tt.stopFn,
			)

			err := c.StopSession(context.Background(), "test-session")
			if (err != nil) != tt.wantErr {
				t.Errorf("StopSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_GetStatus(t *testing.T) {
	t.Parallel()

	c := local.NewClient(
		func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		func(_ context.Context, _ string) error { return nil },
	)

	// Start a session.
	err := c.StartSession(context.Background(), gateway.StartSessionRequest{
		SessionID: "s1",
		TenantID:  "local",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	statuses, err := c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != gateway.SessionActive {
		t.Errorf("got state %v, want active", statuses[0].State)
	}

	// Stop it.
	if err := c.StopSession(context.Background(), "s1"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	statuses, err = c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus after stop: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != gateway.SessionEnded {
		t.Errorf("got state %v, want ended", statuses[0].State)
	}
}

func TestClient_Concurrent(t *testing.T) {
	t.Parallel()

	c := local.NewClient(
		func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		func(_ context.Context, _ string) error { return nil },
	)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sid := fmt.Sprintf("session-%d", i)
			_ = c.StartSession(context.Background(), gateway.StartSessionRequest{SessionID: sid})
			_ = c.StopSession(context.Background(), sid)
			_, _ = c.GetStatus(context.Background())
		}()
	}
	wg.Wait()
}

func TestCallback_NoOp(t *testing.T) {
	t.Parallel()

	cb := &local.Callback{}

	if err := cb.ReportState(context.Background(), "s1", gateway.SessionActive); err != nil {
		t.Errorf("ReportState: %v", err)
	}
	if err := cb.Heartbeat(context.Background(), "s1"); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}
}
