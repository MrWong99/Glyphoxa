package usage

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
)

// ── Mock callback ────────────────────────────────────────────────────────────

type reportStateCall struct {
	sessionID string
	state     gateway.SessionState
	errMsg    string
}

type mockCallback struct {
	reportStateCalls []reportStateCall
	heartbeatCalls   []string
	reportErr        error
	heartbeatErr     error
}

func (m *mockCallback) ReportState(_ context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	m.reportStateCalls = append(m.reportStateCalls, reportStateCall{
		sessionID: sessionID,
		state:     state,
		errMsg:    errMsg,
	})
	return m.reportErr
}

func (m *mockCallback) Heartbeat(_ context.Context, sessionID string) error {
	m.heartbeatCalls = append(m.heartbeatCalls, sessionID)
	return m.heartbeatErr
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestNewRecordingBridge(t *testing.T) {
	t.Parallel()

	inner := &mockCallback{}
	orch := sessionorch.NewMemoryOrchestrator()
	store := NewMemoryStore()

	bridge := NewRecordingBridge(inner, orch, store)
	if bridge == nil {
		t.Fatal("NewRecordingBridge returned nil")
	}
}

func TestRecordingBridge_ReportState_SessionEnded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	inner := &mockCallback{}
	orch := sessionorch.NewMemoryOrchestrator()
	store := NewMemoryStore()

	// Create a session and transition it to active so it has a start time.
	sessionID, err := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	if err := orch.Transition(ctx, sessionID, gateway.SessionActive, ""); err != nil {
		t.Fatalf("Transition to active: %v", err)
	}

	bridge := NewRecordingBridge(inner, orch, store)

	// Report session ended — should record usage and delegate.
	if err := bridge.ReportState(ctx, sessionID, gateway.SessionEnded, "done"); err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	// Verify inner callback was called.
	if len(inner.reportStateCalls) != 1 {
		t.Fatalf("inner.ReportState calls = %d, want 1", len(inner.reportStateCalls))
	}
	call := inner.reportStateCalls[0]
	if call.sessionID != sessionID {
		t.Errorf("sessionID = %q, want %q", call.sessionID, sessionID)
	}
	if call.state != gateway.SessionEnded {
		t.Errorf("state = %v, want SessionEnded", call.state)
	}
	if call.errMsg != "done" {
		t.Errorf("errMsg = %q, want %q", call.errMsg, "done")
	}

	// Verify usage was recorded.
	rec, err := store.GetUsage(ctx, "t1", CurrentPeriod())
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if rec.SessionHours <= 0 {
		// The session was created moments ago, so duration will be tiny but > 0.
		t.Errorf("SessionHours = %g, want > 0", rec.SessionHours)
	}
}

func TestRecordingBridge_ReportState_SessionActive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	inner := &mockCallback{}
	orch := sessionorch.NewMemoryOrchestrator()
	store := NewMemoryStore()

	sessionID, err := orch.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	bridge := NewRecordingBridge(inner, orch, store)

	// Report active state — should delegate without recording usage.
	if err := bridge.ReportState(ctx, sessionID, gateway.SessionActive, ""); err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	// Verify inner callback was called.
	if len(inner.reportStateCalls) != 1 {
		t.Fatalf("inner.ReportState calls = %d, want 1", len(inner.reportStateCalls))
	}
	if inner.reportStateCalls[0].state != gateway.SessionActive {
		t.Errorf("state = %v, want SessionActive", inner.reportStateCalls[0].state)
	}

	// Verify NO usage was recorded.
	rec, err := store.GetUsage(ctx, "t1", CurrentPeriod())
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if rec.SessionHours != 0 {
		t.Errorf("SessionHours = %g, want 0 (no recording for non-ended state)", rec.SessionHours)
	}
}

func TestRecordingBridge_Heartbeat(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	inner := &mockCallback{}
	orch := sessionorch.NewMemoryOrchestrator()
	store := NewMemoryStore()

	bridge := NewRecordingBridge(inner, orch, store)

	if err := bridge.Heartbeat(ctx, "sess-123"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Verify inner callback was called.
	if len(inner.heartbeatCalls) != 1 {
		t.Fatalf("inner.Heartbeat calls = %d, want 1", len(inner.heartbeatCalls))
	}
	if inner.heartbeatCalls[0] != "sess-123" {
		t.Errorf("sessionID = %q, want %q", inner.heartbeatCalls[0], "sess-123")
	}
}
