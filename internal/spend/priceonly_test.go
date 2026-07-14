package spend

import (
	"log/slog"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// priceOnlyBase proves the recorder PriceOnly returns still forwards usage to
// the production base recorder.
type priceOnlyBase struct {
	observe.Discard
	llm int
}

func (b *priceOnlyBase) LLMTokens(observe.Provider, string, int, int) { b.llm++ }

func TestPriceOnly_TeesToBaseAndPrices(t *testing.T) {
	base := &priceOnlyBase{}
	rec, estimatedUSD := PriceOnly(base, slog.New(slog.DiscardHandler))

	if got := estimatedUSD(); got != 0 {
		t.Fatalf("estimate before any usage = %v, want 0", got)
	}

	rec.LLMTokens(observe.ProviderGroq, "openai/gpt-oss-120b", 1_000_000, 1_000_000)
	if base.llm != 1 {
		t.Fatalf("base recorder saw %d LLMTokens calls, want 1 (tee must not swallow usage)", base.llm)
	}
	first := estimatedUSD()
	if first <= 0 {
		t.Fatalf("estimate after usage = %v, want > 0 (priced or fail-closed default, never free)", first)
	}

	rec.LLMTokens(observe.ProviderGroq, "openai/gpt-oss-120b", 1_000_000, 1_000_000)
	if second := estimatedUSD(); second <= first {
		t.Fatalf("estimate after second usage = %v, want > %v (running total)", second, first)
	}
}

// TestPriceOnly_UnknownModelStillPrices pins the fail-closed posture through the
// helper: an unpriced (provider, model) must produce a positive estimate via the
// deliberately HIGH defaults (ADR-0046), never a silent zero.
func TestPriceOnly_UnknownModelStillPrices(t *testing.T) {
	rec, estimatedUSD := PriceOnly(&priceOnlyBase{}, slog.New(slog.DiscardHandler))
	rec.LLMTokens(observe.Provider("nonesuch"), "model-not-in-price-map", 1_000_000, 1_000_000)
	if got := estimatedUSD(); got <= 0 {
		t.Fatalf("estimate for unknown model = %v, want > 0 (fail-closed defaults)", got)
	}
}
