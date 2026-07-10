package presence

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// ConsentButtons handles the rollover-tape disclosure's Consent/Revoke buttons in
// the all/web topology (#306, ADR-0051). A press writes (or deletes) the
// interacting Speaker's consent row, then publishes [voiceevent.TapeConsentChanged]
// on the process-wide bus so a live Voice Session's tape reconciles, and confirms
// with an ephemeral reply. It delegates to [wirenpc.ApplyTapeConsent] so the
// standalone voice-mode client (which has no presence) answers the same buttons
// identically.
type ConsentButtons struct {
	store wirenpc.TapeConsentStore
	bus   *voiceevent.Bus
	log   *slog.Logger
}

// NewConsentButtons builds the handler over the consent store and the process-wide
// voice event bus (the same bus the session Manager publishes on, so the live
// tape's consent subscription reacts). A nil logger discards logs.
func NewConsentButtons(store wirenpc.TapeConsentStore, bus *voiceevent.Bus, log *slog.Logger) *ConsentButtons {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ConsentButtons{store: store, bus: bus, log: log}
}

// HandleComponent is the disgo component-interaction listener. It applies the
// consent change and, for a tape-consent button, replies ephemerally. A non-tape
// button is ignored (another component handler owns it), never answered here.
func (c *ConsentButtons) HandleComponent(e *events.ComponentInteractionCreate) {
	reply, ok := wirenpc.ApplyTapeConsent(context.Background(), c.store, c.bus, time.Now, e.Data.CustomID(), e.User().ID.String())
	if !ok {
		return // not a tape-consent button — leave it for whoever owns it
	}
	if err := e.CreateMessage(ephemeralMessage(reply, true)); err != nil {
		c.log.Warn("presence: reply to tape consent button", "err", err)
	}
}
