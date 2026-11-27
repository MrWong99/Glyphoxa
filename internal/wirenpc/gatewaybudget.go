package wirenpc

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

// GatewayBudgetRecorder classifies Discord gateway session establishments so the
// IDENTIFY budget (#486) is observable. It is satisfied by observe.GatewayBudget;
// declared here as a narrow interface so this package (and internal/presence,
// which reuses it) never imports the concrete recorder just to name it.
type GatewayBudgetRecorder interface {
	// RecordIdentify counts one IDENTIFY sent for the bot application.
	RecordIdentify(appID string)
	// RecordResume counts one reattached session (RESUME) for the bot application.
	RecordResume(appID string)
}

// GatewayBudgetClientOpts returns the disgo options that instrument a client's
// gateway session establishments for the IDENTIFY-budget metrics (#486):
//
//   - an identify rate-limiter wrapper that counts each IDENTIFY at SEND time
//     (when disgo has admitted the identify and the socket to the gateway is
//     open, before any Ready), so a connect that burns the 1000/token/24h budget
//     but never reaches Ready — an InvalidSession reply, a pre-dispatch close, a
//     reconnect loop that keeps re-identifying — is still counted and can trip
//     the alarm. Discord spends the budget on the IDENTIFY it receives, not on
//     the session we establish, so this is where the count belongs. An open
//     whose network dial failed sent nothing and is not counted (#686).
//   - a Resumed event listener that counts each budget-free RESUME.
//
// The identify wrapper also enforces the hard per-application IDENTIFY pacing
// (see [IdentifyPacer]), and comes with a gateway dialer that settles each
// admitted IDENTIFY: counted when the dial succeeds, its pacer token handed
// back when the dial fails (see identifyCounter.dialer). Neither is
// observability and neither depends on b: with a nil b both are still
// installed, counting into a discard recorder, and only the Resumed listener
// is left out.
func GatewayBudgetClientOpts(token string, b GatewayBudgetRecorder) []bot.ConfigOpt {
	appID := applicationIDFromToken(token)
	if b == nil {
		return []bot.ConfigOpt{identifyCounterOpt(newIdentifyCounter(appID, discardGatewayBudget{}))}
	}
	return []bot.ConfigOpt{
		identifyCounterOpt(newIdentifyCounter(appID, b)),
		bot.WithEventListeners(resumeListener(b)),
	}
}

// identifyCounterOpt installs one counter as both the gateway's identify
// rate-limiter and its dialer: the two must be the same counter, the dialer
// settles what that limiter's Wait charged. It replaces a dialer set by an
// earlier gateway option (there is none today), and a later option that
// replaces this one only loses the refund: Unlock then counts the IDENTIFY and
// the pacer keeps its token.
func identifyCounterOpt(c *identifyCounter) bot.ConfigOpt {
	return bot.WithGatewayConfigOpts(
		gateway.WithIdentifyRateLimiter(c),
		gateway.WithDialer(c.dialer((&net.Dialer{}).DialContext)),
	)
}

// discardGatewayBudget is the recorder behind a client built without budget
// observability: the IDENTIFY pacing still applies, the counts go nowhere.
type discardGatewayBudget struct{}

func (discardGatewayBudget) RecordIdentify(string) {}
func (discardGatewayBudget) RecordResume(string)   {}

// resumeListener counts one RESUME per disgo Resumed dispatch, labeled by the
// client's application id (never the token).
func resumeListener(b GatewayBudgetRecorder) bot.EventListener {
	return bot.NewListenerFunc(func(e *events.Resumed) {
		b.RecordResume(e.Client().ApplicationID.String())
	})
}

// identifyCounter wraps a gateway.IdentifyRateLimiter, charges the
// application's [IdentifyPacer] for each IDENTIFY the wrapped limiter admits,
// and counts the ones that may have gone out. The inner limiter is disgo's Noop
// by default, so wrapping it adds the pacer's hold and nothing else to the
// reconnect behavior (backoff).
//
// disgo calls Wait BEFORE it dials, so an admitted IDENTIFY is settled later,
// exactly once, one of two ways:
//
//   - sent (as far as this process can tell): counted in the budget metric,
//     pacer token kept. Decided when the gateway dial succeeds, or at Unlock
//     if the dial never came through this counter's dialer.
//   - provably never sent: not counted, pacer token refunded. A cancelled wait
//     (#671, in the pacer itself) and a failed dial (#686, see dialer).
//
// Counted and charged always go together. The pacer is what holds a runaway
// loop to 484 IDENTIFYs a day, so the metric and the rolling-24h warning stay
// under that bound too — an outage of failed dials neither spends the pacer
// nor raises a token-reset alarm for IDENTIFYs Discord never saw.
//
// One counter serves one gateway.
type identifyCounter struct {
	inner  gateway.IdentifyRateLimiter
	appID  string
	budget GatewayBudgetRecorder
	pacer  *IdentifyPacer
	// pending is true from a Wait the pacer admitted until that IDENTIFY is
	// settled: counted (dial succeeded, or Unlock) or refunded (dial failed).
	pending atomic.Bool
}

func newIdentifyCounter(appID string, b GatewayBudgetRecorder) *identifyCounter {
	return &identifyCounter{
		inner:  gateway.NewNoopIdentifyRateLimiter(),
		appID:  appID,
		budget: b,
		pacer:  identifyPacerFor(appID),
	}
}

func (c *identifyCounter) Close(ctx context.Context) { c.inner.Close(ctx) }

// Wait charges the pacer for an IDENTIFY the inner limiter admits and leaves
// it pending. Nothing is counted here: disgo dials after Wait, and only the
// dial tells whether an IDENTIFY can have gone out. On a context-cancelled
// Wait no identify is sent, so nothing is charged or counted.
func (c *identifyCounter) Wait(ctx context.Context, shardID int) error {
	if err := c.inner.Wait(ctx, shardID); err != nil {
		return err
	}
	// The hard cap comes BEFORE the count: an IDENTIFY that is still waiting
	// for its turn has not been sent.
	if err := c.pacer.Wait(ctx); err != nil {
		return err
	}
	c.pending.Store(true)
	return nil
}

// Unlock ends the open. An IDENTIFY still pending here never saw its dial
// (another dialer was installed over this counter's, or the open failed before
// dialing): it stays charged, so it is counted — the conservative direction.
func (c *identifyCounter) Unlock(shardID int) {
	c.settleSent()
	c.inner.Unlock(shardID)
}

// settleSent counts the pending IDENTIFY, once; its pacer token stays spent.
func (c *identifyCounter) settleSent() {
	if c.pending.CompareAndSwap(true, false) {
		c.budget.RecordIdentify(c.appID)
	}
}

// settleUnsent gives the pending IDENTIFY's pacer token back, once, and does
// not count it.
func (c *identifyCounter) settleUnsent() {
	if c.pending.CompareAndSwap(true, false) {
		c.pacer.Refund()
	}
}

// dialer is the gateway dialer that settles the pending IDENTIFY on the
// outcome of the network dial. disgo calls Wait BEFORE it dials and retries a
// failed open every ten seconds at most, so without the refund every failed
// dial of a network outage spent 3 minutes of budget on an IDENTIFY that never
// left the process and the hold grew without end (#686; the same shape #671
// fixed for cancelled waits). And counted at Wait, those refunded attempts ran
// the metric up at disgo's retry rate with nothing left to bound it.
//
// Only the network dial is covered — DNS and TCP, to Discord or to the proxy
// in front of it: with no socket nothing was sent. Once a socket exists the
// IDENTIFY is counted and stays charged whatever fails afterwards (proxy
// CONNECT, TLS, the HTTP upgrade, the handshake), the conservative direction.
//
// The dial finds its Wait through pending: disgo holds the gateway's conn
// lock from Wait through the dial, so those two never interleave with another
// open of the same gateway, and an open that RESUMEs never calls Wait, so its
// dial settles nothing. The limiter's Unlock runs only after disgo has let
// that lock go; an open that got in between could have its IDENTIFY settled
// as sent by the earlier open's Unlock and miss a refund. That errs to the
// charged-and-counted side, never to a token or a count given away.
//
// dial is what gorilla would use by itself, (&net.Dialer{}).DialContext; the
// rest is its DefaultDialer (proxy from the environment, handshake timeout).
func (c *identifyCounter) dialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *websocket.Dialer {
	d := *websocket.DefaultDialer
	d.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			c.settleUnsent()
			return nil, err
		}
		c.settleSent()
		return conn, nil
	}
	return &d
}

// IDENTIFY pacing: the last line of defence for the Bot token.
//
// Discord allows a token 1000 IDENTIFYs per 24 hours and RESETS the token
// past that. Everything above this point can be wrong about when to
// reconnect — on 2026-11-20 disgo could not decode a GUILD_CREATE carrying a
// channel type it did not know, failed the handshake, and re-identified every
// four seconds inside its own reconnect loop until the production token was
// reset 74 minutes after boot. Nothing in this repository was in that loop to
// stop it. This is the one place every IDENTIFY of every client must pass, so
// the budget is enforced here, per application and process-wide: a small
// burst for boot and ordinary reconnects, then a slow refill. A runaway loop
// is held to [identifyBurst] + 24h/[identifyRefill] = 484 a day whatever its
// cause; a healthy process never notices.
//
// The bucket is charged before the IDENTIFY goes out, so an attempt that
// provably sent none gets its token back: a wait that was cancelled (#671) and
// a gateway dial that failed (#686). Everything else stays charged, and only
// what stays charged is counted in the IDENTIFY metric, so the metric shares
// the bound.
const (
	// identifyBurst is how many IDENTIFYs may go out back to back.
	identifyBurst = 4
	// identifyRefill is how long one further IDENTIFY takes to become available.
	identifyRefill = 3 * time.Minute
)

// IdentifyRefill is how long the pacer takes to make one more IDENTIFY
// available once its burst is spent — the longest a single open waits on it
// when no other open of the same application is queued ahead. Callers that
// bound a gateway open size the bound past it, or such an open can never
// outwait the pacer.
const IdentifyRefill = identifyRefill

// IdentifyPacer is a token bucket over one application's IDENTIFYs. Exported
// so callers that pace their own opens on top of it can be tested against the
// real bucket; production code reaches it only through
// [GatewayBudgetClientOpts].
type IdentifyPacer struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	now    func() time.Time
	appID  string
}

// NewIdentifyPacer builds a full bucket on the given clock.
func NewIdentifyPacer(now func() time.Time) *IdentifyPacer {
	return &IdentifyPacer{tokens: identifyBurst, last: now(), now: now}
}

// identifyPacers holds one pacer per application id for the life of the
// process: the standing presence client and any per-cycle voice clients on the
// same Bot token draw on the same Discord budget, so they share one bucket.
var identifyPacers sync.Map // appID string → *IdentifyPacer

func identifyPacerFor(appID string) *IdentifyPacer {
	if p, ok := identifyPacers.Load(appID); ok {
		return p.(*IdentifyPacer)
	}
	fresh := NewIdentifyPacer(time.Now)
	fresh.appID = appID
	p, _ := identifyPacers.LoadOrStore(appID, fresh)
	return p.(*IdentifyPacer)
}

// refillLocked tops the bucket up for the time since the last call. Caller
// holds mu.
func (p *IdentifyPacer) refillLocked() {
	now := p.now()
	p.tokens = min(identifyBurst, p.tokens+float64(now.Sub(p.last))/float64(identifyRefill))
	p.last = now
}

// Reserve takes one IDENTIFY from the bucket and reports how long the caller
// must wait before sending it; zero means now. The reservation is made either
// way, so concurrent callers queue behind each other instead of all waking for
// the same token.
func (p *IdentifyPacer) Reserve() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refillLocked()
	p.tokens--
	if p.tokens >= 0 {
		return 0
	}
	return time.Duration(-p.tokens * float64(identifyRefill))
}

// Refund returns a reservation whose IDENTIFY was never sent (never past the
// burst). Callers queued behind it keep the waits they were given — the
// conservative direction — but the next reservation is not charged for it.
func (p *IdentifyPacer) Refund() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refillLocked()
	p.tokens = min(identifyBurst, p.tokens+1)
}

// Wait blocks until this caller's IDENTIFY may be sent, or ctx ends. A caller
// that gives up gets its reservation back (no IDENTIFY was sent): keeping it
// meant a bounded open that kept timing out in the pacer spent 3 minutes of
// budget per attempt while the refill gives back one per 3 minutes, so the
// hold grew without end and no later open could ever outwait it.
func (p *IdentifyPacer) Wait(ctx context.Context) error {
	d := p.Reserve()
	if d <= 0 {
		return nil
	}
	slog.Default().Warn("discord gateway IDENTIFY held back to protect the Bot token's 24h budget; something is reconnecting in a loop",
		"app_id", p.appID, "wait", d.Round(time.Second))
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		p.Refund()
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// applicationIDFromToken derives the public application id from a Discord bot
// token's first dot-segment (base64 of the snowflake id), mirroring disgo's own
// derivation so the send-side IDENTIFY label matches the Resumed-event label. It
// returns "" for an unparseable token rather than panicking — the token itself is
// NEVER used as a label or logged.
func applicationIDFromToken(token string) string {
	first, _, _ := strings.Cut(token, ".")
	raw, err := base64.RawStdEncoding.DecodeString(first)
	if err != nil {
		return ""
	}
	id, err := snowflake.Parse(string(raw))
	if err != nil {
		return ""
	}
	return id.String()
}
