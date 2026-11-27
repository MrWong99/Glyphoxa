package wirenpc

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
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

// TestIdentifyCounterCountsAtSend proves an IDENTIFY is counted when the
// gateway's identify rate-limiter has admitted it and the socket to the gateway
// is open — BEFORE any Ready — so a connect that burns the budget but never
// reaches Ready (InvalidSession, pre-dispatch close) is still counted, once.
// Admission alone counts nothing (the dial may still fail, #686), and when the
// inner limiter denies the send (cancelled/deadline ctx), no identify goes out,
// so it counts nothing.
func TestIdentifyCounterCountsAtSend(t *testing.T) {
	spy := &budgetSpy{}
	appID := snowflake.ID(42).String()
	rl := newIdentifyCounter(appID, spy)
	// Its own bucket: on the process-wide one a -count run past the burst
	// waits three minutes per further run.
	rl.pacer = NewIdentifyPacer(time.Now)
	dial := rl.dialer(func(context.Context, string, string) (net.Conn, error) {
		conn, peer := net.Pipe()
		t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
		return conn, nil
	}).NetDialContext

	// Admitted send (default Noop inner admits), not dialed yet → not counted.
	if err := rl.Wait(context.Background(), 0); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if len(spy.identifies) != 0 {
		t.Fatalf("identifies after Wait = %v, want none before the dial", spy.identifies)
	}
	// Socket open → counted, and the end of the open does not count it again.
	if _, err := dial(context.Background(), "tcp", "gateway.discord.gg:443"); err != nil {
		t.Fatalf("dial = %v, want nil", err)
	}
	if len(spy.identifies) != 1 || spy.identifies[0] != appID {
		t.Fatalf("identifies = %v, want [%s]", spy.identifies, appID)
	}
	rl.Unlock(0)
	if len(spy.identifies) != 1 {
		t.Fatalf("identifies after Unlock = %v, want unchanged (1)", spy.identifies)
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

// TestIdentifyCounterCountsAtUnlockWithoutItsDialer: an admitted IDENTIFY whose
// dial never came through the counter's dialer (a later gateway option replaced
// it) keeps its pacer token, so it must still be counted — when the open ends.
func TestIdentifyCounterCountsAtUnlockWithoutItsDialer(t *testing.T) {
	c, _, _ := pacedCounter(t)
	for range identifyBurst {
		waitNow(t, c)
		c.Unlock(0)
	}
	// A stray Unlock settles nothing twice.
	c.Unlock(0)
	if got := counted(c); got != identifyBurst {
		t.Fatalf("counted %d IDENTIFYs, want %d (each one stayed charged)", got, identifyBurst)
	}
	if wait := c.pacer.Reserve(); wait != identifyRefill {
		t.Fatalf("the next IDENTIFY waits %s, want %s (nothing was refunded)", wait, identifyRefill)
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
	p := NewIdentifyPacer(func() time.Time { return clock })

	var sent int
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; {
		wait := p.Reserve()
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
	p := NewIdentifyPacer(func() time.Time { return clock })

	for i := range identifyBurst {
		if wait := p.Reserve(); wait != 0 {
			t.Fatalf("IDENTIFY %d of the burst waited %s, want 0", i+1, wait)
		}
	}
	if wait := p.Reserve(); wait != identifyRefill {
		t.Fatalf("first IDENTIFY past the burst waits %s, want %s", wait, identifyRefill)
	}
	// Reservations queue: the next caller waits behind the previous one.
	if wait := p.Reserve(); wait != 2*identifyRefill {
		t.Fatalf("second IDENTIFY past the burst waits %s, want %s", wait, 2*identifyRefill)
	}

	// A quiet stretch refills the bucket, but never past the burst.
	clock = clock.Add(24 * time.Hour)
	for i := range identifyBurst {
		if wait := p.Reserve(); wait != 0 {
			t.Fatalf("after a quiet day, IDENTIFY %d waited %s, want 0", i+1, wait)
		}
	}
	if wait := p.Reserve(); wait == 0 {
		t.Fatal("the bucket refilled past its burst")
	}
}

func TestIdentifyPacerWaitHonoursContext(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	p := NewIdentifyPacer(func() time.Time { return clock })
	for range identifyBurst {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatalf("wait inside the burst: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait past the burst with a cancelled ctx = %v, want context.Canceled", err)
	}
}

// TestIdentifyPacerRefundsAnAbandonedWait: a caller that gives up before its
// turn sent nothing, so its reservation goes back. Kept, a bounded open that
// kept timing out in the pacer was charged 3 minutes per ~1 minute attempt and
// the hold grew without end (#642 review).
func TestIdentifyPacerRefundsAnAbandonedWait(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	p := NewIdentifyPacer(func() time.Time { return clock })
	for range identifyBurst {
		p.Reserve()
	}
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("abandoned wait = %v, want context.Canceled", err)
		}
	}
	if wait := p.Reserve(); wait != identifyRefill {
		t.Fatalf("after five abandoned waits the next IDENTIFY waits %s, want %s (nothing was sent)", wait, identifyRefill)
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

// pacedCounter is an identifyCounter on its own full pacer with a frozen
// clock (no refill, no process-global bucket), plus the two dial outcomes of
// its gateway dialer.
func pacedCounter(t *testing.T) (c *identifyCounter, dialFails, dialSucceeds func()) {
	t.Helper()
	clock := time.Unix(1_800_000_000, 0)
	c = &identifyCounter{
		inner:  gateway.NewNoopIdentifyRateLimiter(),
		appID:  "app-refund",
		budget: &budgetSpy{},
		pacer:  NewIdentifyPacer(func() time.Time { return clock }),
	}
	errDial := errors.New("dial tcp: network is unreachable")
	dial := func(fail bool) func() {
		d := c.dialer(func(context.Context, string, string) (net.Conn, error) {
			if fail {
				return nil, errDial
			}
			conn, peer := net.Pipe()
			t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
			return conn, nil
		})
		return func() {
			t.Helper()
			conn, err := d.NetDialContext(context.Background(), "tcp", "gateway.discord.gg:443")
			if fail && (!errors.Is(err, errDial) || conn != nil) {
				t.Fatalf("failed dial = (%v, %v), want the dial error passed through", conn, err)
			}
			if !fail && (err != nil || conn == nil) {
				t.Fatalf("dial = (%v, %v), want the conn passed through", conn, err)
			}
		}
	}
	return c, dial(true), dial(false)
}

// counted is how many IDENTIFYs a pacedCounter recorded in the budget metric.
func counted(c *identifyCounter) int { return len(c.budget.(*budgetSpy).identifies) }

// waitNow is a Wait that must be admitted at once: past the burst the pacer
// would hold it for minutes, so a short deadline tells the two apart.
func waitNow(t *testing.T, c *identifyCounter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx, 0); err != nil {
		t.Fatalf("Wait = %v, want an IDENTIFY admitted at once", err)
	}
}

// TestIdentifyCounterRefundsAFailedDial: disgo charges the pacer before it
// dials, and during a network outage every retry fails in the dial — no
// IDENTIFY reaches Discord. Kept, each of those spent 3 minutes of budget and
// the hold grew for as long as the outage lasted (#686). None of them is
// counted either: refunded attempts are no longer held back by the pacer, so
// counting them would run the token-reset alarm up at disgo's retry rate.
func TestIdentifyCounterRefundsAFailedDial(t *testing.T) {
	c, dialFails, _ := pacedCounter(t)
	for range 3 * identifyBurst {
		waitNow(t, c)
		dialFails()
		c.Unlock(0)
	}
	for i := range identifyBurst {
		if wait := c.pacer.Reserve(); wait != 0 {
			t.Fatalf("after an outage of failed dials, IDENTIFY %d of the burst waits %s, want 0 (nothing was sent)", i+1, wait)
		}
	}
	if got := counted(c); got != 0 {
		t.Fatalf("an outage of failed dials counted %d IDENTIFYs, want 0 (nothing was sent)", got)
	}
}

// TestIdentifyCounterKeepsTheChargeOnASuccessfulDial: once the socket exists
// the IDENTIFY may have gone out, whatever becomes of the open afterwards.
func TestIdentifyCounterKeepsTheChargeOnASuccessfulDial(t *testing.T) {
	c, _, dialSucceeds := pacedCounter(t)
	for range identifyBurst {
		waitNow(t, c)
		dialSucceeds()
		c.Unlock(0)
	}
	if wait := c.pacer.Reserve(); wait != identifyRefill {
		t.Fatalf("after a burst of dialed opens the next IDENTIFY waits %s, want %s", wait, identifyRefill)
	}
	if got := counted(c); got != identifyBurst {
		t.Fatalf("a burst of dialed opens counted %d IDENTIFYs, want %d", got, identifyBurst)
	}
}

// TestIdentifyCounterFailedResumeDialRefundsNothing: an open that RESUMEs
// never passes Wait, so its failed dial has no token to give back — and one
// charged open gives back one token however often its dial is seen to fail.
// Counted follows charged throughout: the two dialed opens, not the refunded one.
func TestIdentifyCounterFailedResumeDialRefundsNothing(t *testing.T) {
	c, dialFails, dialSucceeds := pacedCounter(t)

	// Never charged, and charged but already unlocked: nothing to refund.
	dialFails()
	waitNow(t, c)
	dialSucceeds()
	c.Unlock(0)
	dialFails()

	// Charged twice more (three in all), then two failures on the open in
	// progress: exactly one token comes back.
	waitNow(t, c)
	dialSucceeds()
	c.Unlock(0)
	waitNow(t, c)
	dialFails()
	dialFails()
	c.Unlock(0)

	if got := counted(c); got != 2 {
		t.Fatalf("counted %d IDENTIFYs, want 2 (the dialed opens; a RESUME dial and a refunded open count nothing)", got)
	}
	const left = identifyBurst - 2
	for i := range left {
		if wait := c.pacer.Reserve(); wait != 0 {
			t.Fatalf("IDENTIFY %d of the %d left waits %s, want 0", i+1, left, wait)
		}
	}
	if wait := c.pacer.Reserve(); wait != identifyRefill {
		t.Fatalf("with the burst spent the next IDENTIFY waits %s, want %s (a failed dial refunded a token it was never charged)", wait, identifyRefill)
	}
}

// TestGatewayBudgetClientOptsSettleARealOpen drives a real disgo client built
// from GatewayBudgetClientOpts through one gateway open, so the wiring is under
// test and not just the counter: the dialer must be installed, and it must
// settle the IDENTIFY its own limiter charged. disgo's first open comes after
// a 1s backoff and the second 2s later, so the 1.5s bound sees exactly one.
func TestGatewayBudgetClientOptsSettleARealOpen(t *testing.T) {
	// open runs one bounded gateway open against addr for a fresh application
	// (the buckets are process-wide, and -count reruns the test) with a single
	// IDENTIFY left in its bucket, and returns what was counted and how long
	// the next IDENTIFY would wait.
	open := func(t *testing.T, addr string) (identifies int, nextWait time.Duration) {
		t.Helper()
		app := strconv.FormatUint(rand.Uint64N(1<<62), 10)
		restAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"url":"ws://`+addr+`"}`)
		}))
		t.Cleanup(restAPI.Close)

		pacer := identifyPacerFor(app)
		for range identifyBurst - 1 {
			pacer.Reserve()
		}
		spy := &budgetSpy{}
		token := base64.RawStdEncoding.EncodeToString([]byte(app)) + ".x.y"
		opts := append(GatewayBudgetClientOpts(token, spy),
			bot.WithLogger(slog.New(slog.DiscardHandler)),
			// disgo's default REST rate-limiter starts a goroutine it never stops,
			// and the server's own client drops its idle connections on Close.
			bot.WithRestClientConfigOpts(
				rest.WithURL(restAPI.URL),
				rest.WithHTTPClient(restAPI.Client()),
				rest.WithRateLimiter(rest.NewNoopRateLimiter()),
			),
		)
		client, err := disgo.New(token, opts...)
		if err != nil {
			t.Fatalf("disgo.New: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		if err := client.OpenGateway(ctx); err == nil {
			t.Fatal("OpenGateway succeeded against a gateway that cannot answer")
		}
		client.Close(context.Background())
		return len(spy.identifies), pacer.Reserve()
	}

	t.Run("failed dial", func(t *testing.T) {
		t.Parallel()
		// A port nothing listens on: the TCP dial is refused.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		identifies, nextWait := open(t, addr)
		if identifies != 0 {
			t.Fatalf("a refused dial counted %d IDENTIFYs, want 0 (nothing was sent)", identifies)
		}
		if nextWait != 0 {
			t.Fatalf("after a refused dial the next IDENTIFY waits %s, want 0 (the token was not refunded)", nextWait)
		}
	})

	t.Run("dialed", func(t *testing.T) {
		t.Parallel()
		// A socket that opens and hangs up before the websocket upgrade.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()

		identifies, nextWait := open(t, ln.Addr().String())
		if identifies != 1 {
			t.Fatalf("a dialed open counted %d IDENTIFYs, want 1", identifies)
		}
		if nextWait == 0 {
			t.Fatal("after a dialed open the next IDENTIFY does not wait: the token was given back")
		}
	})
}
