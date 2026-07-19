package wirenpc

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
)

// GatewayBudgetRecorder classifies Discord gateway session establishments so the
// IDENTIFY budget (#486) is observable. It is satisfied by observe.GatewayBudget;
// declared here as a narrow interface so this package (and internal/presence,
// which reuses it) never imports the concrete recorder just to name it.
type GatewayBudgetRecorder interface {
	// RecordIdentify counts one fresh session (IDENTIFY) for the bot application.
	RecordIdentify(appID string)
	// RecordResume counts one reattached session (RESUME) for the bot application.
	RecordResume(appID string)
}

// GatewayBudgetListeners returns disgo event listeners that classify each gateway
// session establishment: a Ready dispatch is a fresh IDENTIFY (consumes Discord's
// 1000/token/24h budget), a Resumed dispatch is a RESUME (does not). Both are
// labeled by the client's application id — a public identifier, never the token.
// Returns nil when b is nil so a caller can splat the result unconditionally.
func GatewayBudgetListeners(b GatewayBudgetRecorder) []bot.EventListener {
	if b == nil {
		return nil
	}
	return []bot.EventListener{
		bot.NewListenerFunc(func(e *events.Ready) {
			b.RecordIdentify(e.Client().ApplicationID.String())
		}),
		bot.NewListenerFunc(func(e *events.Resumed) {
			b.RecordResume(e.Client().ApplicationID.String())
		}),
	}
}
