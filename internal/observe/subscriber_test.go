package observe

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// recordingStage captures StageRecorder calls for assertions. Concurrency-safe so
// the -race FirstAudio test can drive it from goroutines.
type recordingStage struct {
	mu            sync.Mutex
	responseLat   []labeledDur
	addressDetect []time.Duration
	ttsTTFB       []labeledDur
	turnOutcomes  []turnOutcomeRec
}

type turnOutcomeRec struct {
	outcome TurnOutcome
	reason  TurnReason
}

type labeledDur struct {
	label string
	d     time.Duration
}

func (r *recordingStage) ResponseLatency(role AgentRole, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responseLat = append(r.responseLat, labeledDur{string(role), d})
}
func (r *recordingStage) AddressDetect(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addressDetect = append(r.addressDetect, d)
}
func (r *recordingStage) TTSTimeToFirstByte(p Provider, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ttsTTFB = append(r.ttsTTFB, labeledDur{string(p), d})
}

// The spans this subscriber does not own are no-ops on the recorder.
func (r *recordingStage) VADHangover(time.Duration)                   {}
func (r *recordingStage) CodecDecode(time.Duration)                   {}
func (r *recordingStage) CodecEncode(time.Duration)                   {}
func (r *recordingStage) STTRequest(Provider, time.Duration)          {}
func (r *recordingStage) TTSTotal(Provider, time.Duration)            {}
func (r *recordingStage) LLMRound(Provider, int, bool, time.Duration) {}
func (r *recordingStage) LLMTurn(Provider, time.Duration)             {}
func (r *recordingStage) ProviderCall(Stage, Provider, Outcome)       {}
func (r *recordingStage) ProviderError(Stage, Provider)               {}
func (r *recordingStage) TurnOutcome(outcome TurnOutcome, reason TurnReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnOutcomes = append(r.turnOutcomes, turnOutcomeRec{outcome, reason})
}

func (r *recordingStage) outcomes() []turnOutcomeRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turnOutcomeRec(nil), r.turnOutcomes...)
}

var _ StageRecorder = (*recordingStage)(nil)

// base is a fixed wall-clock origin for deterministic timestamps.
var base = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func TestStageSubscriberHeadlineAndStages(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec)
	sub.Subscribe(bus)

	speechEnd := base
	bus.Publish(voiceevent.STTFinal{At: base.Add(700 * time.Millisecond), Text: "hi", TurnID: "T1", SpeechEndAt: speechEnd})
	bus.Publish(voiceevent.AddressRouted{At: base.Add(750 * time.Millisecond), TurnID: "T1", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TTSInvoked{At: base.Add(1200 * time.Millisecond), TurnID: "T1", Index: 0})
	bus.Publish(voiceevent.FirstAudio{At: base.Add(1500 * time.Millisecond), TurnID: "T1"})
	// First opus on the wire lands AFTER first-audio-to-pump (codec encode + pacing).
	bus.Publish(voiceevent.FirstOpus{At: base.Add(1600 * time.Millisecond), TurnID: "T1"})

	// response_latency now ends at FirstOpus (audible-on-wire): 1600ms − speechEnd(0)
	// = 1.6s, role=character — NOT the 1.5s handed-to-pump FirstAudio moment.
	if len(rec.responseLat) != 1 || rec.responseLat[0].label != "character" || rec.responseLat[0].d != 1600*time.Millisecond {
		t.Fatalf("response_latency = %+v, want one [character 1.6s] (FirstOpus boundary)", rec.responseLat)
	}
	// address_detect = 750ms − 700ms = 50ms.
	if len(rec.addressDetect) != 1 || rec.addressDetect[0] != 50*time.Millisecond {
		t.Fatalf("address_detect = %v, want [50ms]", rec.addressDetect)
	}
	// tts_ttfb still pairs TTSInvoked↔FirstAudio = 1500ms − 1200ms = 300ms, elevenlabs.
	if len(rec.ttsTTFB) != 1 || rec.ttsTTFB[0].label != "elevenlabs" || rec.ttsTTFB[0].d != 300*time.Millisecond {
		t.Fatalf("tts_ttfb = %+v, want one [elevenlabs 300ms]", rec.ttsTTFB)
	}
}

// TestStageSubscriberFirstOpusBoundary pins task #7: the response_latency span
// ends at FirstOpus (audible-on-wire), and FirstAudio alone does NOT record a
// latency sample — only the success outcome.
func TestStageSubscriberFirstOpusBoundary(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})

	// FirstAudio alone: the turn produced audio (success outcome) but the SLO has
	// not ended yet — no latency sample.
	bus.Publish(voiceevent.FirstAudio{At: base.Add(800 * time.Millisecond), TurnID: "T"})
	if len(rec.responseLat) != 0 {
		t.Fatalf("FirstAudio alone recorded a latency sample %+v; the SLO ends at FirstOpus", rec.responseLat)
	}
	if got := rec.outcomes(); len(got) != 1 || got[0].outcome != TurnFirstAudio {
		t.Fatalf("FirstAudio must record the first_audio success outcome, got %+v", got)
	}

	// FirstOpus on the wire ends the span: 1200 − 0 = 1.2s.
	bus.Publish(voiceevent.FirstOpus{At: base.Add(1200 * time.Millisecond), TurnID: "T"})
	if len(rec.responseLat) != 1 || rec.responseLat[0].d != 1200*time.Millisecond {
		t.Fatalf("response_latency = %+v, want one 1.2s sample at the FirstOpus boundary", rec.responseLat)
	}
}

// TestStageSubscriberFirstOpusNoOpenTurnIgnored proves a FirstOpus for a turn the
// subscriber never opened (STTFinal lost) is a safe no-op.
func TestStageSubscriberFirstOpusNoOpenTurnIgnored(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.FirstOpus{At: base, TurnID: "ghost"}) // no STTFinal first
	if len(rec.responseLat) != 0 {
		t.Fatalf("FirstOpus with no opened turn recorded %+v, want nothing", rec.responseLat)
	}
}

func TestStageSubscriberInterleavedTurnsUseOwnSpeechEnd(t *testing.T) {
	// The whole point of TurnID-keying: two overlapping turns whose FirstAudios
	// arrive out of start order must each pair against their OWN SpeechEndAt.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	endA := base
	endB := base.Add(2 * time.Second)
	bus.Publish(voiceevent.STTFinal{At: base.Add(500 * time.Millisecond), TurnID: "A", SpeechEndAt: endA})
	bus.Publish(voiceevent.AddressRouted{At: base.Add(550 * time.Millisecond), TurnID: "A", Target: voiceevent.AddressTarget{AgentRole: "butler"}})
	bus.Publish(voiceevent.STTFinal{At: base.Add(2500 * time.Millisecond), TurnID: "B", SpeechEndAt: endB})
	bus.Publish(voiceevent.AddressRouted{At: base.Add(2550 * time.Millisecond), TurnID: "B", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	// B's audio reaches the wire FIRST (its LLM was faster), then A's. The SLO
	// ends at FirstOpus per turn, each anchored to its OWN speechEnd.
	bus.Publish(voiceevent.FirstOpus{At: base.Add(3 * time.Second), TurnID: "B"})         // 3000 − 2000 = 1.0s
	bus.Publish(voiceevent.FirstOpus{At: base.Add(3500 * time.Millisecond), TurnID: "A"}) // 3500 − 0 = 3.5s

	got := map[string]time.Duration{}
	for _, l := range rec.responseLat {
		got[l.label] = l.d
	}
	if got["character"] != 1*time.Second {
		t.Errorf("turn B (character) latency = %v, want 1s (own speechEnd)", got["character"])
	}
	if got["butler"] != 3500*time.Millisecond {
		t.Errorf("turn A (butler) latency = %v, want 3.5s (own speechEnd)", got["butler"])
	}
}

func TestStageSubscriberHeadlineExactlyOnce(t *testing.T) {
	// A multi-sentence turn: each sentence has its own playback Source, so each
	// publishes its own FirstAudio (feeds tts_ttfb) AND its own FirstOpus (the
	// per-Source once-guard — see wire.TestPlaySentenceBus_TwoSentencesPublishPerSource).
	// The headline-SLO dedup lives HERE: only the FIRST FirstOpus per turn sets the
	// response_latency sample (latencyDone). Three sentences ⇒ one sample.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i+1) * 100 * time.Millisecond)
		bus.Publish(voiceevent.TTSInvoked{At: at, TurnID: "T", Index: i})
		bus.Publish(voiceevent.FirstAudio{At: at.Add(50 * time.Millisecond), TurnID: "T"})
		bus.Publish(voiceevent.FirstOpus{At: at.Add(80 * time.Millisecond), TurnID: "T"})
	}
	if len(rec.responseLat) != 1 {
		t.Fatalf("response_latency fired %d times, want exactly 1 (first FirstOpus only)", len(rec.responseLat))
	}
	if len(rec.ttsTTFB) != 3 {
		t.Fatalf("tts_ttfb fired %d times, want 3 (one per sentence)", len(rec.ttsTTFB))
	}
}

func TestStageSubscriberTTFBPairsLatestNotStale(t *testing.T) {
	// A zero-chunk sentence emits TTSInvoked but never a FirstAudio (TTSInvoked is
	// published unconditionally, FirstAudio only on a real chunk). The next
	// sentence's tts_ttfb must pair against ITS OWN invoke, not the stale
	// zero-chunk one — FIFO-front would mismeasure it.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})

	// Sentence 0: synthesized but zero chunks → TTSInvoked, no FirstAudio.
	bus.Publish(voiceevent.TTSInvoked{At: base.Add(100 * time.Millisecond), TurnID: "T", Index: 0})
	// Sentence 1: serial dispatch means its invoke comes AFTER sentence 0's, then
	// its FirstAudio. Correct ttfb = 1200 − 1000 = 200ms (its own invoke).
	bus.Publish(voiceevent.TTSInvoked{At: base.Add(1000 * time.Millisecond), TurnID: "T", Index: 1})
	bus.Publish(voiceevent.FirstAudio{At: base.Add(1200 * time.Millisecond), TurnID: "T"})

	if len(rec.ttsTTFB) != 1 {
		t.Fatalf("tts_ttfb fired %d times, want 1", len(rec.ttsTTFB))
	}
	if rec.ttsTTFB[0].d != 200*time.Millisecond {
		t.Fatalf("tts_ttfb = %v, want 200ms (paired to sentence 1's own invoke, not the stale 100ms one)", rec.ttsTTFB[0].d)
	}
}

func TestStageSubscriberZeroSpeechEndSkipped(t *testing.T) {
	// A flushed utterance with no speech-end transition (SpeechEndAt zero) must
	// record NO response_latency — a decades-long delta would wreck the p95.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", Text: "flush"}) // SpeechEndAt zero
	bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "butler"}})
	bus.Publish(voiceevent.FirstAudio{At: base.Add(time.Second), TurnID: "T"})
	bus.Publish(voiceevent.FirstOpus{At: base.Add(1100 * time.Millisecond), TurnID: "T"})

	if len(rec.responseLat) != 0 {
		t.Fatalf("response_latency = %+v, want none (zero SpeechEndAt skipped)", rec.responseLat)
	}
}

func TestStageSubscriberSweepReapsAbandonedTurns(t *testing.T) {
	// A turn that emits TTSInvoked but never a FirstAudio (barged/errored before
	// audio) records no sample and its state is reaped by the TTL sweep.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec)
	sub.Subscribe(bus)

	clock := base
	sub.now = func() time.Time { return clock }

	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "dead", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock, TurnID: "dead", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TTSInvoked{At: clock, TurnID: "dead", Index: 0})
	// never a FirstAudio.

	if got := sub.Sweep(); got != 0 {
		t.Fatalf("premature reap: %d (turn still within TTL)", got)
	}
	clock = base.Add(2 * defaultTurnTTL) // advance past TTL
	if got := sub.Sweep(); got != 1 {
		t.Fatalf("reaped %d, want 1 (abandoned turn)", got)
	}
	if len(rec.responseLat) != 0 {
		t.Fatalf("abandoned turn recorded a sample: %+v", rec.responseLat)
	}
	// The survivorship counterpart: the reaped no-audio turn is counted as
	// abandoned so the failure is visible even though response_latency saw nothing.
	if got := rec.outcomes(); len(got) != 1 || got[0].outcome != TurnAbandoned || got[0].reason != ReasonNoFirstAudio {
		t.Fatalf("turn outcomes = %+v, want one [abandoned no_first_audio]", got)
	}
	// A second sweep is a no-op (state already gone).
	if got := sub.Sweep(); got != 0 {
		t.Fatalf("double-reap: %d", got)
	}
}

// TestStageSubscriberFirstAudioOutcome pins the success counterpart: a turn that
// reaches first audio records exactly one first_audio outcome, and a later reap
// of its (kept-alive) state does NOT re-count it as abandoned.
func TestStageSubscriberFirstAudioOutcome(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec)
	sub.Subscribe(bus)

	clock := base
	sub.now = func() time.Time { return clock }

	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "live", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock.Add(50 * time.Millisecond), TurnID: "live", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TTSInvoked{At: clock.Add(400 * time.Millisecond), TurnID: "live", Index: 0})
	bus.Publish(voiceevent.FirstAudio{At: clock.Add(600 * time.Millisecond), TurnID: "live"})
	// A second sentence's first audio must NOT re-count the outcome.
	bus.Publish(voiceevent.TTSInvoked{At: clock.Add(900 * time.Millisecond), TurnID: "live", Index: 1})
	bus.Publish(voiceevent.FirstAudio{At: clock.Add(1000 * time.Millisecond), TurnID: "live"})

	if got := rec.outcomes(); len(got) != 1 || got[0].outcome != TurnFirstAudio || got[0].reason != ReasonNone {
		t.Fatalf("turn outcomes = %+v, want one [first_audio none]", got)
	}

	// Reaping the completed turn's kept-alive state must not add an abandoned count.
	clock = base.Add(2 * defaultTurnTTL)
	sub.Sweep()
	if got := rec.outcomes(); len(got) != 1 {
		t.Fatalf("reap of a completed turn re-counted the outcome: %+v", got)
	}
}

// TestStageSubscriberYieldedOutcome pins the coalesced-segment path: a
// TurnEnded(supersede_coalesced) records the distinct `yielded` outcome (not
// abandoned), and a later TTL reap of its state must not re-count it.
func TestStageSubscriberYieldedOutcome(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec)
	sub.Subscribe(bus)

	clock := base
	sub.now = func() time.Time { return clock }

	// A late segment of one over-split utterance: opened, routed, then coalesced.
	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "late", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock, TurnID: "late", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TurnEnded{At: clock, TurnID: "late", Reason: voiceevent.TurnEndSupersedeCoalesced, Text: "and have you seen Gandalf"})

	if got := rec.outcomes(); len(got) != 1 || got[0].outcome != TurnYielded || got[0].reason != ReasonSupersessionGrace {
		t.Fatalf("turn outcomes = %+v, want one [yielded supersession_grace]", got)
	}
	// No response_latency sample for a yielded (unspoken) segment.
	if len(rec.responseLat) != 0 {
		t.Fatalf("yielded segment recorded a latency sample: %+v", rec.responseLat)
	}
	// Reaping the yielded turn's state must not re-count it as abandoned.
	clock = base.Add(2 * defaultTurnTTL)
	sub.Sweep()
	if got := rec.outcomes(); len(got) != 1 {
		t.Fatalf("reap of a yielded turn re-counted the outcome: %+v", got)
	}
}

// TestStageSubscriberTurnEndedReasons pins the precise-reason mapping: each
// TurnEnded reason records the right outcome+reason, and a turn-end arriving
// AFTER first audio is ignored (the turn already counted first_audio).
func TestStageSubscriberTurnEndedReasons(t *testing.T) {
	cases := []struct {
		name        string
		reason      voiceevent.TurnEndReason
		wantOutcome TurnOutcome
		wantReason  TurnReason
	}{
		{"barge", voiceevent.TurnEndBarge, TurnAbandoned, ReasonBarge},
		{"tts_error", voiceevent.TurnEndTTSError, TurnAbandoned, ReasonTTSError},
		{"provider_error", voiceevent.TurnEndProviderError, TurnAbandoned, ReasonProviderError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingStage{}
			bus := voiceevent.NewBus()
			NewStageSubscriber(rec).Subscribe(bus)

			bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
			bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})
			bus.Publish(voiceevent.TurnEnded{At: base, TurnID: "T", Reason: tc.reason})

			if got := rec.outcomes(); len(got) != 1 || got[0].outcome != tc.wantOutcome || got[0].reason != tc.wantReason {
				t.Fatalf("outcomes = %+v, want one [%s %s]", got, tc.wantOutcome, tc.wantReason)
			}
		})
	}
}

// TestStageSubscriberTurnEndedAfterFirstAudioIgnored proves a barge mid-playback
// (after first audio) does not re-count the turn: first_audio is terminal.
func TestStageSubscriberTurnEndedAfterFirstAudioIgnored(t *testing.T) {
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base.Add(50 * time.Millisecond), TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.FirstAudio{At: base.Add(600 * time.Millisecond), TurnID: "T"})
	// Barge after audio began: normal interruption, not a failure.
	bus.Publish(voiceevent.TurnEnded{At: base.Add(900 * time.Millisecond), TurnID: "T", Reason: voiceevent.TurnEndBarge})

	if got := rec.outcomes(); len(got) != 1 || got[0].outcome != TurnFirstAudio {
		t.Fatalf("outcomes = %+v, want one [first_audio none] (post-audio barge must not re-count)", got)
	}
}

// TestStageSubscriberTurnLog pins the per-turn timing trace: WithTurnLog emits
// exactly one INFO line per turn-end, carrying the timing spine — and, for a
// turn that never produced audio, the no_audio marker that makes a 20s
// self-cancel debuggable from the logs (the trace Sprint-2 cleanup removed).
func TestStageSubscriberTurnLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec, WithTurnLog(log))
	sub.Subscribe(bus)

	clock := base
	sub.now = func() time.Time { return clock }

	// A surviving turn: one line with first_audio timing.
	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "ok", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock.Add(50 * time.Millisecond), TurnID: "ok", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.FirstAudio{At: clock.Add(600 * time.Millisecond), TurnID: "ok"})

	// An abandoned turn (no audio): reaped and logged with the no_audio marker.
	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "dead", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock.Add(50 * time.Millisecond), TurnID: "dead", Target: voiceevent.AddressTarget{AgentRole: "character"}})

	// A coalesced (yielded) segment: logged at turn-end with its dropped transcript
	// — the residual a yielded turn loses until real utterance coalescing.
	bus.Publish(voiceevent.STTFinal{At: clock, TurnID: "late", SpeechEndAt: clock})
	bus.Publish(voiceevent.AddressRouted{At: clock.Add(50 * time.Millisecond), TurnID: "late", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TurnEnded{At: clock.Add(60 * time.Millisecond), TurnID: "late", Reason: voiceevent.TurnEndSupersedeCoalesced, Text: "have you seen Gandalf"})

	clock = base.Add(2 * defaultTurnTTL)
	sub.Sweep()

	out := buf.String()
	if got := strings.Count(out, "voice turn end"); got != 3 {
		t.Fatalf("turn-end log lines = %d, want 3\n%s", got, out)
	}
	if !strings.Contains(out, `turn_id=ok`) || !strings.Contains(out, "first_audio_after=600ms") {
		t.Fatalf("surviving turn line missing turn_id/first_audio_after:\n%s", out)
	}
	if !strings.Contains(out, `turn_id=dead`) || !strings.Contains(out, "no_audio=true") {
		t.Fatalf("abandoned turn line missing turn_id/no_audio marker:\n%s", out)
	}
	if !strings.Contains(out, `turn_id=late`) || !strings.Contains(out, "outcome=yielded") || !strings.Contains(out, "Gandalf") {
		t.Fatalf("yielded turn line missing turn_id/outcome/dropped transcript:\n%s", out)
	}
}

func TestStageSubscriberConcurrentFirstAudioRace(t *testing.T) {
	// FirstAudio (tee goroutine) and FirstOpus (disgo sender goroutine) both publish
	// off non-subscriber goroutines — concurrent deliveries across turns. -race
	// guards the shared per-turn map.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	const n = 50
	for i := 0; i < n; i++ {
		id := turnID(i)
		bus.Publish(voiceevent.STTFinal{At: base, TurnID: id, SpeechEndAt: base})
		bus.Publish(voiceevent.AddressRouted{At: base, TurnID: id, Target: voiceevent.AddressTarget{AgentRole: "character"}})
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bus.Publish(voiceevent.FirstAudio{At: base.Add(time.Second), TurnID: turnID(i)})
			bus.Publish(voiceevent.FirstOpus{At: base.Add(1100 * time.Millisecond), TurnID: turnID(i)})
		}(i)
	}
	wg.Wait()

	if len(rec.responseLat) != n {
		t.Fatalf("got %d response_latency samples, want %d (one per turn)", len(rec.responseLat), n)
	}
}

func turnID(i int) string {
	return "turn-" + string(rune('A'+i/26)) + string(rune('a'+i%26))
}

func TestStageSubscriberStartReapsOnTicker(t *testing.T) {
	// Start runs Sweep on a ticker; with a short TTL an abandoned turn is reaped
	// without anyone calling Sweep directly (the live leak guard).
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	sub := NewStageSubscriber(rec)
	sub.ttl = 20 * time.Millisecond
	sub.Subscribe(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub.Start(ctx)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "dead", SpeechEndAt: base})
	bus.Publish(voiceevent.TTSInvoked{At: base, TurnID: "dead"}) // no FirstAudio ⇒ abandoned

	deadline := time.Now().Add(2 * time.Second)
	for {
		sub.mu.Lock()
		n := len(sub.turns)
		sub.mu.Unlock()
		if n == 0 {
			return // reaped by the ticker
		}
		if time.Now().After(deadline) {
			t.Fatal("Start ticker never reaped the abandoned turn")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStageSubscriberEndToEndPrometheus(t *testing.T) {
	// Gate wording literally: drive a real Bus into the real Prometheus adapter and
	// assert the response_latency histogram actually gains a sample on scrape (also
	// guards against a label-cardinality mismatch at the subscriber→adapter seam).
	rec := NewPrometheusRecorder()
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base.Add(40 * time.Millisecond), TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	bus.Publish(voiceevent.TTSInvoked{At: base.Add(900 * time.Millisecond), TurnID: "T"})
	bus.Publish(voiceevent.FirstAudio{At: base.Add(1100 * time.Millisecond), TurnID: "T"})
	bus.Publish(voiceevent.FirstOpus{At: base.Add(1200 * time.Millisecond), TurnID: "T"})

	out := scrape(t, rec)
	wants := []string{
		`glyphoxa_voice_response_latency_seconds_count{agent_role="character"} 1`,
		`glyphoxa_voice_address_detect_seconds_count 1`,
		`glyphoxa_voice_tts_ttfb_seconds_count{provider="elevenlabs"} 1`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("scrape missing %q\n%s", w, filterGlyphoxa(out))
		}
	}
}
