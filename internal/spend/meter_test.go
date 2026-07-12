package spend

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

func f64(v float64) *float64 { return &v }

// approx compares two USD figures with a cent-scale tolerance so floating point
// accumulation never fails an otherwise-correct expectation.
func approx(t *testing.T, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("estimated USD = %.12f, want %.12f", got, want)
	}
}

// TestMeterKnownLLMPrice pins the known (groq, llama-3.3-70b-versatile) rate: the
// estimate is the per-direction token count times the code-const per-1M rate.
func TestMeterKnownLLMPrice(t *testing.T) {
	m := NewMeter(Caps{}, nil, nil, nil)
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)
	want := llmPrices[llmKey{observe.ProviderGroq, "llama-3.3-70b-versatile"}]
	approx(t, m.Status().EstimatedUSD, want.inputPerMTok+want.outputPerMTok)
}

// TestMeterGroqCatalogPricesKnown pins the #424/#426 Groq tool-capable catalog:
// each model resolves to a code-const rate and the recap/no-price WARN class
// cannot fire for it. openai/gpt-oss-120b is the new deployment default (#424),
// so a miss here would re-introduce the every-recap conservative-estimate warn.
func TestMeterGroqCatalogPricesKnown(t *testing.T) {
	for _, model := range []string{
		"openai/gpt-oss-120b",
		"openai/gpt-oss-20b",
		"meta-llama/llama-4-scout-17b-16e-instruct",
		"qwen/qwen3-32b",
	} {
		t.Run(model, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			m := NewMeter(Caps{}, log, nil, nil)
			m.LLMTokens(observe.ProviderGroq, model, 1_000_000, 1_000_000)

			want, ok := llmPrices[llmKey{observe.ProviderGroq, model}]
			if !ok {
				t.Fatalf("no price entry for groq/%s", model)
			}
			approx(t, m.Status().EstimatedUSD, want.inputPerMTok+want.outputPerMTok)
			if strings.Contains(buf.String(), "no price") {
				t.Fatalf("groq/%s logged the conservative-default warn:\n%s", model, buf.String())
			}
		})
	}
}

// TestMeterGeminiImagePrice: a generated image (#311) is metered as Gemini LLM
// output tokens and priced from the known image-model entry (no default warning).
func TestMeterGeminiImagePrice(t *testing.T) {
	m := NewMeter(Caps{}, nil, nil, nil)
	// One image ≈ 1290 output tokens plus a small prompt.
	m.LLMTokens(observe.ProviderGemini, "gemini-2.5-flash-image", 40, 1290)
	want := llmPrices[llmKey{observe.ProviderGemini, "gemini-2.5-flash-image"}]
	expected := 40.0/1e6*want.inputPerMTok + 1290.0/1e6*want.outputPerMTok
	approx(t, m.Status().EstimatedUSD, expected)
}

// TestMeterUnknownModelWarnsOnce uses a model with no price entry: it falls back
// to the conservative default AND logs exactly one warning even across repeats.
func TestMeterUnknownModelWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m := NewMeter(Caps{}, log, nil, nil)

	m.LLMTokens(observe.ProviderGroq, "mystery-model", 1_000_000, 0)
	m.LLMTokens(observe.ProviderGroq, "mystery-model", 1_000_000, 0)

	// Conservative default rate applied twice (input only).
	approx(t, m.Status().EstimatedUSD, 2*defaultLLMInputPerMTok)

	if n := strings.Count(buf.String(), "no price"); n != 1 {
		t.Fatalf("unknown-model warnings = %d, want exactly 1\nlog:\n%s", n, buf.String())
	}
}

// TestMeterTTSAndSTTAccumulate proves the TTS-character and STT-audio-second
// capture points both fold into the same accumulator.
func TestMeterTTSAndSTTAccumulate(t *testing.T) {
	m := NewMeter(Caps{}, nil, nil, nil)
	m.TTSCharacters(observe.ProviderElevenLabs, 1000)
	m.STTAudioSeconds(observe.ProviderElevenLabs, time.Hour)
	want := ttsPricePer1kChars[observe.ProviderElevenLabs] + sttPricePerHour[observe.ProviderElevenLabs]
	approx(t, m.Status().EstimatedUSD, want)
}

// TestMeterSoftCapTripsOnce: a soft-only cap flips state to soft, fires onSoft
// exactly once, and refuses new turns thereafter — in-flight semantics live at
// the replier, the meter only reports the gate.
func TestMeterSoftCapTripsOnce(t *testing.T) {
	var softN, hardN int
	m := NewMeter(Caps{SoftUSD: f64(1.0)}, nil, func() { softN++ }, func() { hardN++ })

	if !m.AllowTurn() {
		t.Fatal("AllowTurn false before any spend")
	}
	// Cross the soft cap with an STT second priced above $1 via the default? Use a
	// known priced call sized to cross 1 USD: 1M+1M groq tokens > $1.
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)

	if m.AllowTurn() {
		t.Fatal("AllowTurn true after crossing the soft cap")
	}
	if got := m.Status().State; got != CapSoft {
		t.Fatalf("state = %q, want soft", got)
	}
	if softN != 1 {
		t.Fatalf("onSoft fired %d times, want 1", softN)
	}
	if hardN != 0 {
		t.Fatalf("onHard fired %d times, want 0 (no hard cap set)", hardN)
	}
}

// TestMeterHardCapTripsOnce: a hard-only cap flips state to hard and fires onHard
// once.
func TestMeterHardCapTripsOnce(t *testing.T) {
	var softN, hardN int
	m := NewMeter(Caps{HardUSD: f64(0.5)}, nil, func() { softN++ }, func() { hardN++ })

	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)
	// Idempotent: further spend does not re-fire.
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)

	if m.AllowTurn() {
		t.Fatal("AllowTurn true after crossing the hard cap")
	}
	if got := m.Status().State; got != CapHard {
		t.Fatalf("state = %q, want hard", got)
	}
	if hardN != 1 {
		t.Fatalf("onHard fired %d times, want 1", hardN)
	}
	if softN != 0 {
		t.Fatalf("onSoft fired %d times, want 0 (no soft cap set)", softN)
	}
}

// TestMeterBothCapsGradual: soft then hard fire in order as spend climbs past each.
func TestMeterBothCapsGradual(t *testing.T) {
	var order []string
	var mu sync.Mutex
	rec := func(s string) func() { return func() { mu.Lock(); order = append(order, s); mu.Unlock() } }
	m := NewMeter(Caps{SoftUSD: f64(1.0), HardUSD: f64(2.5)}, nil, rec("soft"), rec("hard"))

	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000) // ~1.38
	if got := m.Status().State; got != CapSoft {
		t.Fatalf("after first add state = %q, want soft", got)
	}
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000) // ~2.76 > 2.5
	if got := m.Status().State; got != CapHard {
		t.Fatalf("after second add state = %q, want hard", got)
	}
	if strings.Join(order, ",") != "soft,hard" {
		t.Fatalf("callback order = %v, want [soft hard]", order)
	}
}

// TestMeterNoCapsNeverTrips: with neither cap set the meter accumulates but never
// gates or fires — today's behavior.
func TestMeterNoCapsNeverTrips(t *testing.T) {
	var fired int
	m := NewMeter(Caps{}, nil, func() { fired++ }, func() { fired++ })
	m.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 10_000_000, 10_000_000)
	if !m.AllowTurn() {
		t.Fatal("AllowTurn false with no caps set")
	}
	if m.Status().State != CapNone {
		t.Fatalf("state = %q, want none", m.Status().State)
	}
	if fired != 0 {
		t.Fatalf("callbacks fired %d times with no caps, want 0", fired)
	}
}

var _ observe.UsageSink = (*Meter)(nil)
