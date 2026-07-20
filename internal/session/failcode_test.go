package session_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/session"
)

// TestControlFailureRoundtrip covers sequence (4): the three typed control
// refusals cross the plane as coded last_error strings and decode back to the
// SAME sentinel, so both deployment shapes surface identical errors.
func TestControlFailureRoundtrip(t *testing.T) {
	for _, sentinel := range []error{
		session.ErrNoActiveSession,
		session.ErrAgentNotInCampaign,
		session.ErrButlerVoiceless,
	} {
		encoded := session.EncodeControlFailure(fmt.Errorf("wrap: %w", sentinel))
		got, ok := session.DecodeControlFailure(encoded)
		if !ok || !errors.Is(got, sentinel) {
			t.Errorf("roundtrip(%v) = (%v, %v), want the sentinel back", sentinel, got, ok)
		}
	}
}

// TestControlFailureUnknown: an untyped error encodes as its raw message (no
// code) and decodes to ok=false — the requester surfaces it verbatim.
func TestControlFailureUnknown(t *testing.T) {
	encoded := session.EncodeControlFailure(errors.New("tts provider exploded"))
	if encoded != "tts provider exploded" {
		t.Fatalf("encoded unknown = %q, want the raw message", encoded)
	}
	if got, ok := session.DecodeControlFailure(encoded); ok || got != nil {
		t.Fatalf("decode unknown = (%v, %v), want (nil, false)", got, ok)
	}
}
