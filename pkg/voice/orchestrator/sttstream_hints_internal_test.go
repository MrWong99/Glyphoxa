package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// hintRecorder is a dialScript that also keeps every dial's StreamConfig.
type hintRecorder struct {
	*dialScript
	cfgs chan stt.StreamConfig
}

func (h *hintRecorder) OpenStream(ctx context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	h.cfgs <- cfg
	return h.dialScript.OpenStream(ctx, cfg)
}

// TestStreamManager_DialsWithItsRecognitionHints: the terms and the language the
// manager was built with reach EVERY session it dials — the first one and each
// redial, since a provider fixes them per connection (#701).
func TestStreamManager_DialsWithItsRecognitionHints(t *testing.T) {
	rec := &hintRecorder{dialScript: newDialScript(), cfgs: make(chan stt.StreamConfig, 8)}
	clock := newLifeClock()
	terms := []string{"Flo", "Bart"}
	m := NewStreamManager(rec, WithStreamTerms(terms), WithStreamLanguage("de"))
	bindLifecycle(t, m, rec.dialScript, clock)

	clock.advance(time.Minute)
	rec.kill(0, idleClose())
	rec.awaitDial(t)

	for dial := range 2 {
		cfg := <-rec.cfgs
		if !reflect.DeepEqual(cfg.Terms, terms) {
			t.Errorf("dial %d: Terms = %q, want %q", dial, cfg.Terms, terms)
		}
		if cfg.Language != "de" {
			t.Errorf("dial %d: Language = %q, want de", dial, cfg.Language)
		}
	}
}

// TestStreamManager_NoHints_DialsBare: a manager built without hints dials the
// pre-#701 configuration.
func TestStreamManager_NoHints_DialsBare(t *testing.T) {
	rec := &hintRecorder{dialScript: newDialScript(), cfgs: make(chan stt.StreamConfig, 8)}
	m := NewStreamManager(rec)
	bindLifecycle(t, m, rec.dialScript, newLifeClock())

	if cfg := <-rec.cfgs; len(cfg.Terms) != 0 || cfg.Language != "" {
		t.Errorf("bare manager dialled with Terms=%q Language=%q", cfg.Terms, cfg.Language)
	}
}

// TestStreamManager_TermsRefused_RedialsOnTheOrdinarySchedule: a session the
// provider refused over its keyterms is NOT a durable misconfiguration — the
// adapter has stopped sending them (it fails open), so the redial is expected to
// work and must not be parked at the backoff cap. It waits the initial backoff,
// like any session that died young, so a provider that keeps refusing is still
// not redialled in a tight loop.
func TestStreamManager_TermsRefused_RedialsOnTheOrdinarySchedule(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second), WithStreamTerms([]string{"Flo"}))
	bindLifecycle(t, m, d, clock)

	d.kill(0, &stt.StreamError{Code: stt.CodeTermsRefused, Fatal: true, Err: errors.New("keyterms refused")})
	d.awaitDial(t)
	if got := clock.slept(); len(got) != 1 || got[0] != time.Second {
		t.Errorf("sleeps = %v, want one 1s wait (the initial backoff), not the 30s cap", got)
	}
}

// TestStreamManager_InvalidRequest_WaitsAtTheCap: a configuration the provider
// refuses for a reason the adapter cannot remove will be refused again on the
// next dial, so the manager waits at the backoff cap instead of redialling fast
// — the same posture as a rejected key.
func TestStreamManager_InvalidRequest_WaitsAtTheCap(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second))
	bindLifecycle(t, m, d, clock)

	d.kill(0, &stt.StreamError{Code: "invalid_request", Fatal: true, Err: errors.New("Too many keyterms")})
	d.awaitDial(t)
	if got := clock.slept(); len(got) != 1 || got[0] != 30*time.Second {
		t.Errorf("sleeps = %v, want one 30s wait at the cap", got)
	}
}
