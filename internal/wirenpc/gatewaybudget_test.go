package wirenpc

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// budgetSpy captures the classified session-establishment calls.
type budgetSpy struct {
	identifies []string
	resumes    []string
}

func (s *budgetSpy) RecordIdentify(appID string) { s.identifies = append(s.identifies, appID) }
func (s *budgetSpy) RecordResume(appID string)   { s.resumes = append(s.resumes, appID) }

// denyLimiter is an inner rate-limiter whose Wait always denies (as a real
// identify limiter does on a cancelled/deadline-exceeded context), so no identify
// is sent.
type denyLimiter struct{ err error }

func (d denyLimiter) Close(context.Context)           {}
func (d denyLimiter) Wait(context.Context, int) error { return d.err }
func (denyLimiter) Unlock(int)                        {}

// TestIdentifyCounterCountsAtSend proves an IDENTIFY is counted when the gateway's
// identify rate-limiter admits a send — BEFORE any Ready — so a connect that
// burns the budget but never reaches Ready (InvalidSession, pre-dispatch close)
// is still counted. When the inner limiter denies the send (cancelled/deadline
// ctx), no identify goes out, so it counts nothing.
func TestIdentifyCounterCountsAtSend(t *testing.T) {
	spy := &budgetSpy{}
	appID := snowflake.ID(42).String()
	rl := newIdentifyCounter(appID, spy)

	// Admitted send (default Noop inner admits) → counted.
	if err := rl.Wait(context.Background(), 0); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if len(spy.identifies) != 1 || spy.identifies[0] != appID {
		t.Fatalf("identifies = %v, want [%s]", spy.identifies, appID)
	}

	// Denied send → no identify sent → no count.
	denied := &identifyCounter{inner: denyLimiter{err: context.Canceled}, appID: appID, budget: spy}
	if err := denied.Wait(context.Background(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(denied) = %v, want context.Canceled", err)
	}
	if len(spy.identifies) != 1 {
		t.Fatalf("identifies after denied Wait = %v, want unchanged (1)", spy.identifies)
	}
}

// TestResumeListenerCounts proves a disgo Resumed dispatch is classified as a
// RESUME labeled by the client's application id, and a Ready dispatch is NOT
// counted here (identify is counted at send time, not on Ready).
func TestResumeListenerCounts(t *testing.T) {
	spy := &budgetSpy{}
	l := resumeListener(spy)

	appID := snowflake.ID(7)
	client := &bot.Client{ApplicationID: appID}
	gen := events.NewGenericEvent(client, 0, 0)
	l.OnEvent(&events.Resumed{GenericEvent: gen})
	l.OnEvent(&events.Ready{GenericEvent: gen})

	if len(spy.resumes) != 1 || spy.resumes[0] != appID.String() {
		t.Fatalf("resumes = %v, want [%s]", spy.resumes, appID)
	}
	if len(spy.identifies) != 0 {
		t.Fatalf("identifies = %v, want none (identify is send-side)", spy.identifies)
	}
}

// TestGatewayBudgetClientOptsBranching proves the borrowed path (nil budget)
// attaches NOTHING, while the owned path (budget set) attaches the instrumentation
// opts — so a borrowed standing client is never double-counted.
func TestGatewayBudgetClientOptsBranching(t *testing.T) {
	if got := GatewayBudgetClientOpts("tok", nil); got != nil {
		t.Fatalf("GatewayBudgetClientOpts(nil budget) = %d opts, want 0 (borrowed client attaches nothing)", len(got))
	}
	if got := GatewayBudgetClientOpts("tok", &budgetSpy{}); len(got) == 0 {
		t.Fatalf("GatewayBudgetClientOpts(budget) attached nothing, want the instrumentation opts (owned client)")
	}
}

// TestApplicationIDFromToken proves the application id is derived from the token's
// first segment (base64 of the snowflake), matching disgo's own derivation, and
// degrades to "" for an unparseable token rather than panicking.
func TestApplicationIDFromToken(t *testing.T) {
	// snowflake 42 → base64("42") = "NDI"; a real token is "<appid-b64>.<ts>.<hmac>".
	if got := applicationIDFromToken("NDI.abc.def"); got != "42" {
		t.Fatalf("applicationIDFromToken = %q, want %q", got, "42")
	}
	if got := applicationIDFromToken("!!!not-base64"); got != "" {
		t.Fatalf("applicationIDFromToken(bad) = %q, want empty", got)
	}
}
