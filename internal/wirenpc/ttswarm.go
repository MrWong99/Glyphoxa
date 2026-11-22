package wirenpc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// newCycleSynth builds one cycle's outbound synthesizer: the ElevenLabs client
// for the resolved TTS key (#69: the decrypted BYOK key, or "" to fall back to
// ELEVENLABS_API_KEY) on the configured transport ([Config.TTSTransport], #702),
// wrapped in the [wire.TeeSynthesizer] that feeds sink, with the client's
// connection kept warm for the cycle ([wireTTSWarm], #702). Building both here
// keeps the pre-warm on the very client the cycle synthesizes on, so the
// synthesizer cannot be wired without it. A keyless client warms nothing.
//
// release ends the cycle's hold on the client: it removes the warm
// subscriptions and closes the client, which on the websocket transport shuts
// its dialogue sockets (each holds one of the account's dialogue-session slots
// while open). The caller defers it; ctx bounds every warm.
func newCycleSynth(ctx context.Context, bus *voiceevent.Bus, ttsKey string, transport ttseleven.Transport, sink wire.PlaybackSink, log *slog.Logger) (synth *wire.TeeSynthesizer, release func()) {
	client := newTTS(ttsKey, ttseleven.WithTransport(transport))
	unwire := wireTTSWarm(ctx, bus, client, log)
	return wire.NewTeeSynthesizer(client, sink, bus), func() {
		unwire()
		_ = client.Close()
	}
}

// ttsWarmer is the synthesis-connection pre-warm the ElevenLabs adapter offers
// ([ttseleven.Client.Warm]).
type ttsWarmer interface {
	Warm(ctx context.Context) error
}

// ttsWarmQuiet is how long the synthesis pool must have been idle before a
// speech start warms it. Inside the window a warm or a streaming sentence already
// left a live pooled connection (one idle 45 s was measured still reused), and a
// warm's HEAD (~130–145 ms round trip against the real API, versus ~55–80 ms for a
// fresh TCP+TLS dial) would occupy the one idle connection the reply's first
// sentence is about to need. It also bounds the rate: one anonymous HEAD per
// utterance start would be ~2000 per 3 h table session from the fleet egress IP.
const ttsWarmQuiet = 20 * time.Second

// wireTTSWarm keeps the synthesizer's connection warm for the cycle (#702): it
// warms once now (the cycle just connected), on every [voiceevent.BargeDetected]
// (the cut sentence's stream took its connection down with it) and on a
// [voiceevent.VADSpeechStart] (a reply's first sentence follows within the
// STT+LLM time) when the pool has been quiet for [ttsWarmQuiet] — no warm
// dispatched and no [voiceevent.FirstAudio] (a sentence streaming) inside it.
// With eleven_v4_turbo answering in ~135 ms on a warm connection, a dial on the
// first sentence is the largest avoidable cost.
//
// Warms run off the bus goroutine (bus callbacks must not block) and single-
// flight: a trigger arriving while a warm is in flight is dropped, since the one
// in flight already produces the warm connection. Failures are advisory — the
// synthesis would simply dial — and logged at debug; a failed warm does not count
// as pool activity. The returned func removes the subscriptions; ctx bounds every
// warm.
func wireTTSWarm(ctx context.Context, bus *voiceevent.Bus, w ttsWarmer, log *slog.Logger) (unsubscribe func()) {
	return wireTTSWarmClock(ctx, bus, w, log, time.Now)
}

// wireTTSWarmClock is [wireTTSWarm] on an injectable clock.
func wireTTSWarmClock(ctx context.Context, bus *voiceevent.Bus, w ttsWarmer, log *slog.Logger, now func() time.Time) (unsubscribe func()) {
	var inFlight atomic.Bool
	var lastActive atomic.Int64 // unix nanos of the last warm dispatch or FirstAudio; 0 = never
	warm := func(trigger string) {
		if !inFlight.CompareAndSwap(false, true) {
			return
		}
		stamp := now().UnixNano()
		lastActive.Store(stamp)
		go func() {
			defer inFlight.Store(false)
			if err := w.Warm(ctx); err != nil {
				lastActive.CompareAndSwap(stamp, 0)
				if log != nil && ctx.Err() == nil {
					log.Debug("tts: connection pre-warm failed", "trigger", trigger, "err", err)
				}
			}
		}()
	}
	warm("connect")
	offSpeech := voiceevent.On(bus, func(voiceevent.VADSpeechStart) {
		if last := lastActive.Load(); last != 0 && now().Sub(time.Unix(0, last)) < ttsWarmQuiet {
			return
		}
		warm("speech_start")
	})
	offAudio := voiceevent.On(bus, func(voiceevent.FirstAudio) { lastActive.Store(now().UnixNano()) })
	offBarge := voiceevent.On(bus, func(voiceevent.BargeDetected) { warm("barge") })
	return func() {
		offSpeech()
		offAudio()
		offBarge()
	}
}
