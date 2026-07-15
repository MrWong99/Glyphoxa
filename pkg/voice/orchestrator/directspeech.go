package orchestrator

import (
	"context"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// VoiceLookup resolves an Agent's TTS [tts.Voice] for a GM /say (#295), reporting
// ok=false when the Agent has no resolvable Voice (an unknown id, or a text-only
// Agent). The [DirectSpeech] reactor renders the /say text with the returned Voice.
// Production binds it to the wirenpc Roster (Roster.Voice); tests inject a closure.
type VoiceLookup func(agentID string) (tts.Voice, bool)

// DirectSpeech is the [Reactor] that turns a [voiceevent.SpeakRequested] (a GM
// `/say <text> as:<agent>`) into a single TTS dispatch in the Agent's Voice — GM
// puppeteering (ADR-0010, ADR-0024). It is deliberately NOT driven off
// [voiceevent.AddressRouted]: /say bypasses Address Detection and the LLM Replier
// entirely, so it must never publish AddressRouted (which would wake the Replier).
//
// It mirrors the barge-in [Replier] turn mechanics (ADR-0027): the turn runs on its
// own goroutine under the SAME shared [Floor], so a human barge yields that floor
// and cancels the /say playback mid-sentence exactly as it cancels an LLM turn. The
// resulting [TTSInvoked] / [FirstAudio] / [FirstOpus] carry the request's TurnID, so
// the transcript relay assembles the /say line through its normal TTSInvoked
// projection — no hand-crafted transcript row (ADR-0012/0040).
//
// Two deliberate divergences from the LLM path:
//   - The GM mute is BYPASSED: /say is a GM override, so a muted NPC still speaks
//     (there is no MuteView here, by design).
//   - The whole text is ONE dispatch (no sentence split), so a long /say pays the
//     full first-audio latency; accepted for v1.0 (a puppeted line is short).
//
// The spend gate is still honored (a session past its soft cap refuses new turns).
//
// Known residuals (per plan #295, accepted for v1.0):
//   - The /say line does NOT enter the target NPC's [Replier] conversation history:
//     it never runs the Agent loop, so the NPC will not "remember" a puppeted line
//     on its next LLM turn. The GM is voicing the NPC, not teaching it.
//   - Silent-success drops: the GM's slash reply acks BEFORE this reactor runs (the
//     handler does not Defer), so the three no-audio outcomes here — a voiceOf miss,
//     a spend-cap refusal, and a floor coalesce-fold — end the turn quietly after the
//     ack. The coalesce fold matters because [Floor.Take]'s window keys on the target
//     agent id ONLY and the floor is SHARED with the [Replier]: a /say as Bart landing
//     within the coalesce window of Bart's own in-flight LLM turn is folded into that
//     turn and never spoken. Rare (a GM /say racing the NPC's own reply); a TurnEnded
//     records it for the metrics subscriber.
type DirectSpeech struct {
	tts     *TTS
	voiceOf VoiceLookup
	onError ErrorFunc

	// floor, when non-nil, is the SHARED barge-in floor the turn runs under so a
	// human interruption cancels the /say (ADR-0027). Set by [Conversation.Register]
	// from the same floor the barge path uses; nil dispatches synchronously (no
	// barge). Not part of [NewDirectSpeech].
	floor *Floor

	// gate, when non-nil, is the live turn gate (#130): a session past its soft cap
	// refuses the /say turn before taking the floor. Set by [Conversation.Register]
	// from [Barge.Gate]; nil is the feature-off default. Not part of [NewDirectSpeech].
	gate TurnGate
}

// NewDirectSpeech wires ttsStage and voiceOf together. Both must be non-nil;
// passing nil for either panics. onError may be nil (a [TTS.Dispatch] failure is
// then dropped silently, mirroring [NewReplier]).
func NewDirectSpeech(ttsStage *TTS, voiceOf VoiceLookup, onError ErrorFunc) *DirectSpeech {
	if ttsStage == nil {
		panic("orchestrator.NewDirectSpeech: tts must not be nil")
	}
	if voiceOf == nil {
		panic("orchestrator.NewDirectSpeech: voiceOf must not be nil")
	}
	return &DirectSpeech{tts: ttsStage, voiceOf: voiceOf, onError: onError}
}

// Bind subscribes the reactor to [voiceevent.SpeakRequested] on bus and returns a
// function that removes the subscription. It implements [Reactor]; bus must be
// non-nil.
func (d *DirectSpeech) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.DirectSpeech.Bind: bus must not be nil")
	}
	// The shared floor publishes the cut turn's TurnEnded{superseded} when a Take
	// supersedes a live holder (#443) — idempotent with the Replier's install.
	if d.floor != nil {
		d.floor.bindSupersedeTerminal(bus)
	}
	return voiceevent.On(bus, func(e voiceevent.SpeakRequested) {
		// Carry the turn correlation id (A3) so the TTS stage and wire tee stamp the
		// same id on TTSInvoked / FirstAudio — installed before the floor is taken so
		// both the sync and barge-in branches inherit it, exactly like the Replier.
		turnCtx := voiceevent.WithTurnID(ctx, e.TurnID)

		// voiceOf miss: the Agent has no resolvable Voice (unknown id / text-only). End
		// the turn with an error reason rather than panicking on a zero Voice.
		voice, ok := d.voiceOf(e.Target.AgentID)
		if !ok {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndProviderError})
			return
		}

		// Spend soft cap (#130): refuse a NEW turn before taking the floor. A SINGLE
		// pre-check is airtight — spend is monotonic. The GM MUTE is deliberately not
		// consulted here (puppeteering overrides mute).
		if d.gate != nil && !d.gate.AllowTurn() {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSpendCap})
			return
		}

		// No floor wired (voice standalone / bench): dispatch synchronously on the bus
		// goroutine, mirroring the Replier's no-floor branch.
		if d.floor == nil {
			d.dispatch(turnCtx, bus, e, voice)
			return
		}

		// Barge-in: take the shared floor and run the turn on its own goroutine so the
		// inbound loop keeps feeding VAD during playback, and a barge yielding the floor
		// cancels floorCtx (unwinding TTS + playback). The take carries the target agent
		// id, which is what [Floor.Take]'s coalesce window keys on (holderAgent only) —
		// and the floor is SHARED with the [Replier]. So a /say as Bart landing inside
		// the coalesce window of Bart's own in-flight LLM turn is folded into that turn
		// and NOT spoken: a silent-success drop after the GM's ack (see the type doc's
		// residuals). We honor the fold rather than supersede — publishing a TurnEnded
		// so the metrics subscriber records the dropped segment — because superseding
		// would cancel the NPC's live LLM reply mid-sentence, a worse outcome.
		floorCtx, release, coalesced := d.floor.Take(turnCtx, e.Target.AgentID)
		if coalesced {
			release()
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSupersedeCoalesced, Text: e.Text})
			return
		}
		go func() {
			defer release()
			d.dispatch(floorCtx, bus, e, voice)
		}()
	})
}

// dispatch renders one /say request's whole text as a single dispatch through the
// turn module (#444). A start error (real synth/provider fault, not a barge
// cancel) that produced no audio is announced as a tts_error TurnEnded so the
// metrics subscriber records the precise reason; a barge cutting ctx publishes
// its own TurnEnded, so it is not double-counted here.
func (d *DirectSpeech) dispatch(ctx context.Context, bus *voiceevent.Bus, e voiceevent.SpeakRequested, voice tts.Voice) {
	t := newTurnRun(ctx, d.tts.Dispatch, d.onError)
	_ = t.dispatch(Reply{Sentence: e.Text, Voice: voice})
	if t.ttsFailed && ctx.Err() == nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndTTSError})
	}
}
