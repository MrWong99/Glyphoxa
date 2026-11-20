package wirenpc

import (
	"context"
	"errors"
	"testing"
	"time"

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
	// No recorder: the IDENTIFY pacing is still installed (it protects the
	// token, it is not observability), the Resumed listener is not.
	if got := GatewayBudgetClientOpts("tok", nil); len(got) != 1 {
		t.Fatalf("GatewayBudgetClientOpts(nil budget) = %d opts, want 1 (the IDENTIFY pacer alone)", len(got))
	}
	if got := GatewayBudgetClientOpts("tok", &budgetSpy{}); len(got) != 2 {
		t.Fatalf("GatewayBudgetClientOpts(budget) = %d opts, want 2 (pacer+counter, Resumed listener)", len(got))
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

// TestIdentifyPacerCapsARunawayLoop is the regression test for the token reset
// of 2026-11-20: a reconnect loop that IDENTIFYs every four seconds for a day.
// The pacer must hold what actually goes out far inside Discord's 1000 per 24h.
func TestIdentifyPacerCapsARunawayLoop(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	p := newIdentifyPacer(func() time.Time { return clock })

	var sent int
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; {
		wait := p.reserve()
		// The loop cannot ask again before its held-back IDENTIFY went out.
		clock = clock.Add(wait)
		elapsed += wait
		if elapsed >= 24*time.Hour {
			break
		}
		sent++
		clock = clock.Add(4 * time.Second)
		elapsed += 4 * time.Second
	}

	if sent > 500 {
		t.Fatalf("a day-long reconnect loop sent %d IDENTIFYs; Discord resets the token at 1000", sent)
	}
	if sent < identifyBurst {
		t.Fatalf("only %d IDENTIFYs went out; the burst must pass untouched", sent)
	}
}

func TestIdentifyPacerBurstThenRefill(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	p := newIdentifyPacer(func() time.Time { return clock })

	for i := range identifyBurst {
		if wait := p.reserve(); wait != 0 {
			t.Fatalf("IDENTIFY %d of the burst waited %s, want 0", i+1, wait)
		}
	}
	if wait := p.reserve(); wait != identifyRefill {
		t.Fatalf("first IDENTIFY past the burst waits %s, want %s", wait, identifyRefill)
	}
	// Reservations queue: the next caller waits behind the previous one.
	if wait := p.reserve(); wait != 2*identifyRefill {
		t.Fatalf("second IDENTIFY past the burst waits %s, want %s", wait, 2*identifyRefill)
	}

	// A quiet stretch refills the bucket, but never past the burst.
	clock = clock.Add(24 * time.Hour)
	for i := range identifyBurst {
		if wait := p.reserve(); wait != 0 {
			t.Fatalf("after a quiet day, IDENTIFY %d waited %s, want 0", i+1, wait)
		}
	}
	if wait := p.reserve(); wait == 0 {
		t.Fatal("the bucket refilled past its burst")
	}
}

func TestIdentifyPacerWaitHonoursContext(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	p := newIdentifyPacer(func() time.Time { return clock })
	for range identifyBurst {
		if err := p.wait(context.Background()); err != nil {
			t.Fatalf("wait inside the burst: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait past the burst with a cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestIdentifyPacerIsSharedPerApplication(t *testing.T) {
	a1, a2, b := identifyPacerFor("app-pacer-a"), identifyPacerFor("app-pacer-a"), identifyPacerFor("app-pacer-b")
	if a1 != a2 {
		t.Fatal("two clients of one application got separate IDENTIFY budgets")
	}
	if a1 == b {
		t.Fatal("two applications share one IDENTIFY budget")
	}
}
