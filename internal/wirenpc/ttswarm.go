package wirenpc

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// ttsWarmer is the synthesis-connection pre-warm the ElevenLabs adapter offers
// ([ttseleven.Client.Warm]).
type ttsWarmer interface {
	Warm(ctx context.Context) error
}

// wireTTSWarm keeps the synthesizer's connection warm for the cycle (#702): it
// warms once now (the cycle just connected), on every [voiceevent.VADSpeechStart]
// (a reply's first sentence follows within the STT+LLM time) and on every
// [voiceevent.BargeDetected] (the cut sentence's stream took its connection down
// with it). With eleven_v4_turbo answering in ~135 ms on a warm connection, a
// dial (~60–130 ms TCP+TLS) on the first sentence is the largest avoidable cost.
//
// Warms run off the bus goroutine (bus callbacks must not block) and single-
// flight: a trigger arriving while a warm is in flight is dropped, since the one
// in flight already produces the warm connection. Failures are advisory — the
// synthesis would simply dial — and logged at debug. The returned func removes
// the subscriptions; ctx bounds every warm.
func wireTTSWarm(ctx context.Context, bus *voiceevent.Bus, w ttsWarmer, log *slog.Logger) (unsubscribe func()) {
	var inFlight atomic.Bool
	warm := func(trigger string) {
		if !inFlight.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer inFlight.Store(false)
			if err := w.Warm(ctx); err != nil && log != nil && ctx.Err() == nil {
				log.Debug("tts: connection pre-warm failed", "trigger", trigger, "err", err)
			}
		}()
	}
	warm("connect")
	offSpeech := voiceevent.On(bus, func(voiceevent.VADSpeechStart) { warm("speech_start") })
	offBarge := voiceevent.On(bus, func(voiceevent.BargeDetected) { warm("barge") })
	return func() {
		offSpeech()
		offBarge()
	}
}
