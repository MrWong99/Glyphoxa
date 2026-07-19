package wirenpc

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
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
//     (when disgo admits the identify, before any Ready), so a connect that burns
//     the 1000/token/24h budget but never reaches Ready — an InvalidSession reply,
//     a pre-dispatch close, a reconnect loop that keeps re-identifying — is still
//     counted and can trip the alarm. Discord spends the budget on the IDENTIFY it
//     receives, not on the session we establish, so this is where the count belongs.
//   - a Resumed event listener that counts each budget-free RESUME.
//
// Returns nil when b is nil so a caller (the borrowed standing-client path, or an
// env-only bench) can splat the result unconditionally and attach nothing — a
// borrowed client is instrumented by the presence that owns it, never twice.
func GatewayBudgetClientOpts(token string, b GatewayBudgetRecorder) []bot.ConfigOpt {
	if b == nil {
		return nil
	}
	appID := applicationIDFromToken(token)
	return []bot.ConfigOpt{
		bot.WithGatewayConfigOpts(gateway.WithIdentifyRateLimiter(newIdentifyCounter(appID, b))),
		bot.WithEventListeners(resumeListener(b)),
	}
}

// resumeListener counts one RESUME per disgo Resumed dispatch, labeled by the
// client's application id (never the token).
func resumeListener(b GatewayBudgetRecorder) bot.EventListener {
	return bot.NewListenerFunc(func(e *events.Resumed) {
		b.RecordResume(e.Client().ApplicationID.String())
	})
}

// identifyCounter wraps a gateway.IdentifyRateLimiter and counts one IDENTIFY each
// time the wrapped limiter admits a send (Wait returns nil). The inner limiter is
// disgo's Noop by default, so wrapping it counts without changing the identify
// pacing — the reconnect behavior (backoff) is unchanged.
type identifyCounter struct {
	inner  gateway.IdentifyRateLimiter
	appID  string
	budget GatewayBudgetRecorder
}

func newIdentifyCounter(appID string, b GatewayBudgetRecorder) gateway.IdentifyRateLimiter {
	return &identifyCounter{
		inner:  gateway.NewNoopIdentifyRateLimiter(),
		appID:  appID,
		budget: b,
	}
}

func (c *identifyCounter) Close(ctx context.Context) { c.inner.Close(ctx) }

// Wait counts an IDENTIFY only when the inner limiter admits the send: on a
// context-cancelled Wait no identify is sent, so nothing is counted.
func (c *identifyCounter) Wait(ctx context.Context, shardID int) error {
	if err := c.inner.Wait(ctx, shardID); err != nil {
		return err
	}
	c.budget.RecordIdentify(c.appID)
	return nil
}

func (c *identifyCounter) Unlock(shardID int) { c.inner.Unlock(shardID) }

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
