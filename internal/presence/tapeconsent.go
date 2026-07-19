package presence

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// ConsentButtons handles the rollover-tape disclosure's Consent/Revoke buttons in
// the all/web topology (#306, ADR-0051). A press writes (or deletes) the
// interacting Speaker's consent row, then routes [voiceevent.TapeConsentChanged]
// into the live Voice Session running the button's Campaign (#487) so that
// session's tape reconciles, and confirms with an ephemeral reply. It delegates to
// [wirenpc.ApplyTapeConsent] so the standalone voice-mode client (which has no
// presence) answers the same buttons identically.
type ConsentButtons struct {
	store wirenpc.TapeConsentStore
	pub   wirenpc.TapeConsentPublisher
	log   *slog.Logger
}

// NewConsentButtons builds the handler over the consent store and the
// session-routing publisher (#487): pub is the *session.Registry, which routes the
// consent event onto the bus of whichever live session runs the button's Campaign
// (and, via Forward, onto the process bus stamped). A nil logger discards logs.
func NewConsentButtons(store wirenpc.TapeConsentStore, pub wirenpc.TapeConsentPublisher, log *slog.Logger) *ConsentButtons {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ConsentButtons{store: store, pub: pub, log: log}
}

// HandleComponent is the disgo component-interaction listener. It applies the
// consent change and, for a tape-consent button, replies ephemerally. A non-tape
// button is ignored (another component handler owns it), never answered here.
func (c *ConsentButtons) HandleComponent(e *events.ComponentInteractionCreate) {
	reply, ok := wirenpc.ApplyTapeConsent(context.Background(), c.store, c.pub, time.Now, c.log, e.Data.CustomID(), e.User().ID.String())
	if !ok {
		return // not a tape-consent button — leave it for whoever owns it
	}
	if err := e.CreateMessage(ephemeralMessage(reply, true)); err != nil {
		c.log.Warn("presence: reply to tape consent button", "err", err)
	}
}
