package voice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

// TestSessionCloseBoundsTheLeave (T3): conn.Close sends the LEAVE voice-state
// update under the ctx it is given, and Session.Close holds playMu meanwhile —
// an unbounded ctx let a stuck main gateway wedge every later Play and Close.
func TestSessionCloseBoundsTheLeave(t *testing.T) {
	sess, conn := openSession(t)
	sess.Close()

	deadline, ok := conn.CloseDeadline()
	if !ok {
		t.Fatal("Session.Close gave conn.Close a ctx with no deadline; a stuck LEAVE would hold playMu forever")
	}
	if left := time.Until(deadline); left > sessionLeaveTimeout {
		t.Errorf("LEAVE deadline %s away, want at most %s", left, sessionLeaveTimeout)
	}
}

// TestSessionPlayAfterProviderClosedFails (T4): since disgo v0.20.0 the audio
// sender closes its provider when the sender itself shuts down. A Play after
// that installed a slot nothing would ever poll, so its Done never closed and
// a PlaybackPump waiting on it wedged. It must fail instead.
func TestSessionPlayAfterProviderClosedFails(t *testing.T) {
	sess, _ := openSession(t)
	defer sess.Close()

	sess.provider.Close() // what disgo's sender does as it exits

	pb, err := sess.Play(context.Background(), endlessSource{})
	if err == nil {
		t.Fatal("Play after the sender closed the provider succeeded; its Done would never close")
	}
	if !errors.Is(err, ErrSenderClosed) {
		t.Errorf("err = %v, want ErrSenderClosed", err)
	}
	if pb != nil {
		t.Errorf("Play returned a playback %v alongside the error", pb)
	}
}

// TestDiscardForgetsSpeakingEvents (T5): speakingEvents is process-global and
// keyed by voice gateway; a discarded conn's gateway is unreachable, so its
// entry must go with it rather than wait for the age-based prune.
func TestDiscardForgetsSpeakingEvents(t *testing.T) {
	var rec stateRecorder
	var removed atomic.Int32
	conn, _ := guardedTestConn(t, &rec, &removed, make(chan time.Time))

	gw := conn.Gateway()
	noteSpeakingEvent(gw, voice.OpcodeSpeaking, 0, voice.GatewayMessageDataSpeaking{SSRC: 7, Speaking: voice.SpeakingFlagMicrophone})
	if speakingEvents.last(gw).IsZero() {
		t.Fatal("setup: remote speaking was not recorded")
	}

	conn.Close(context.Background())

	if !speakingEvents.last(gw).IsZero() {
		t.Error("the discarded conn's gateway is still in speakingEvents")
	}
}
