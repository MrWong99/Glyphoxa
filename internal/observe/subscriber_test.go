package observe

import (
	"context"
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

	// response_latency = firstAudio(1500ms) − speechEnd(0) = 1.5s, role=character.
	if len(rec.responseLat) != 1 || rec.responseLat[0].label != "character" || rec.responseLat[0].d != 1500*time.Millisecond {
		t.Fatalf("response_latency = %+v, want one [character 1.5s]", rec.responseLat)
	}
	// address_detect = 750ms − 700ms = 50ms.
	if len(rec.addressDetect) != 1 || rec.addressDetect[0] != 50*time.Millisecond {
		t.Fatalf("address_detect = %v, want [50ms]", rec.addressDetect)
	}
	// tts_ttfb = 1500ms − 1200ms = 300ms, elevenlabs.
	if len(rec.ttsTTFB) != 1 || rec.ttsTTFB[0].label != "elevenlabs" || rec.ttsTTFB[0].d != 300*time.Millisecond {
		t.Fatalf("tts_ttfb = %+v, want one [elevenlabs 300ms]", rec.ttsTTFB)
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
	// B's audio lands FIRST (its LLM was faster), then A's.
	bus.Publish(voiceevent.FirstAudio{At: base.Add(3 * time.Second), TurnID: "B"})         // 3000 − 2000 = 1.0s
	bus.Publish(voiceevent.FirstAudio{At: base.Add(3500 * time.Millisecond), TurnID: "A"}) // 3500 − 0 = 3.5s

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
	// Multiple sentences ⇒ multiple FirstAudio for one turn: response_latency must
	// fire ONCE (first), the rest feed tts_ttfb only.
	rec := &recordingStage{}
	bus := voiceevent.NewBus()
	NewStageSubscriber(rec).Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{At: base, TurnID: "T", SpeechEndAt: base})
	bus.Publish(voiceevent.AddressRouted{At: base, TurnID: "T", Target: voiceevent.AddressTarget{AgentRole: "character"}})
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i+1) * 100 * time.Millisecond)
		bus.Publish(voiceevent.TTSInvoked{At: at, TurnID: "T", Index: i})
		bus.Publish(voiceevent.FirstAudio{At: at.Add(50 * time.Millisecond), TurnID: "T"})
	}
	if len(rec.responseLat) != 1 {
		t.Fatalf("response_latency fired %d times, want exactly 1", len(rec.responseLat))
	}
	if len(rec.ttsTTFB) != 3 {
		t.Fatalf("tts_ttfb fired %d times, want 3 (one per sentence)", len(rec.ttsTTFB))
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
	// A second sweep is a no-op (state already gone).
	if got := sub.Sweep(); got != 0 {
		t.Fatalf("double-reap: %d", got)
	}
}

func TestStageSubscriberConcurrentFirstAudioRace(t *testing.T) {
	// FirstAudio publishes off the tee's forward goroutine — concurrent deliveries
	// across turns. -race guards the shared per-turn map.
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
