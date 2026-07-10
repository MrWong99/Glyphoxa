package presence

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TapeConsentStore is the persistence the rollover-tape consent buttons need
// (#306, ADR-0051): a row per (Campaign, Speaker) is the durable record of
// consent. *storage.Store satisfies it structurally.
type TapeConsentStore interface {
	UpsertTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error
	DeleteTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error
}

// ConsentButtons handles the rollover-tape disclosure's Consent/Revoke buttons
// (#306, ADR-0051). A press writes (or deletes) the interacting Speaker's consent
// row, then publishes [voiceevent.TapeConsentChanged] on the process-wide bus so a
// live Voice Session's tape arms or clears that Speaker's lane, and confirms with
// an ephemeral reply visible only to the presser. The DB write happens BEFORE the
// event (the MuteChanged ordering precedent), so a reactor reading storage on the
// event always sees the change.
type ConsentButtons struct {
	store TapeConsentStore
	bus   *voiceevent.Bus
	log   *slog.Logger
	now   func() time.Time
}

// NewConsentButtons builds the handler over the consent store and the process-wide
// voice event bus (the same bus the session Manager publishes on, so the live
// tape's consent subscription reacts). A nil logger discards logs.
func NewConsentButtons(store TapeConsentStore, bus *voiceevent.Bus, log *slog.Logger) *ConsentButtons {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ConsentButtons{store: store, bus: bus, log: log, now: time.Now}
}

// HandleComponent is the disgo component-interaction listener. It recovers the
// interacting Speaker's Discord id and the button's custom id, applies the consent
// change, and — for a tape-consent button — replies ephemerally. A non-tape button
// is ignored (another component handler owns it), never answered here.
func (c *ConsentButtons) HandleComponent(e *events.ComponentInteractionCreate) {
	customID := e.Data.CustomID()
	userID := e.User().ID.String()
	reply, ok := c.apply(context.Background(), customID, userID)
	if !ok {
		return // not a tape-consent button — leave it for whoever owns it
	}
	if err := e.CreateMessage(ephemeralMessage(reply, true)); err != nil {
		c.log.Warn("presence: reply to tape consent button", "err", err)
	}
}

// apply is the transport-agnostic core (mirrors the command dispatch split): it
// parses the custom id, writes the consent change, publishes the event, and
// returns the ephemeral confirmation. ok is false for a custom id that is not a
// tape-consent button, so the listener ignores it. A storage failure still returns
// ok=true with an apologetic reply (the button WAS ours) and publishes nothing —
// the durable state did not change, so neither must the live tape.
func (c *ConsentButtons) apply(ctx context.Context, customID, userID string) (reply string, ok bool) {
	campaignID, granted, ok := wirenpc.ParseTapeConsentCustomID(customID)
	if !ok {
		return "", false
	}

	if granted {
		if err := c.store.UpsertTapeConsent(ctx, campaignID, userID); err != nil {
			c.log.Error("presence: upsert tape consent", "campaign", campaignID, "err", err)
			return "Sorry — could not record your consent. Please try again.", true
		}
	} else {
		if err := c.store.DeleteTapeConsent(ctx, campaignID, userID); err != nil {
			c.log.Error("presence: delete tape consent", "campaign", campaignID, "err", err)
			return "Sorry — could not update your choice. Please try again.", true
		}
	}

	// Publish AFTER the durable write (ADR-0051; MuteChanged ordering precedent), so
	// a live session's tape.SetConsent reacts to a state that is already persisted.
	if c.bus != nil {
		c.bus.Publish(voiceevent.TapeConsentChanged{
			At:         c.now(),
			CampaignID: campaignID.String(),
			SpeakerID:  userID,
			Granted:    granted,
		})
	}

	if granted {
		return "You're now included in Session Highlights for this campaign. You can press **Revoke** any time to opt out.", true
	}
	return "You've opted out of Session Highlights. Your audio won't be recorded, and anything already buffered is discarded.", true
}
