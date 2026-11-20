package mapgen_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/mapgen"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// A generated map lands in the Tenant's Usage Ledger under the image model it
// was generated with (#658): Gemini bills an image as output tokens, so the
// row sits under the llm component with the model as the discriminator.
func TestGenerate_ReachesTheUsageLedger(t *testing.T) {
	t.Parallel()
	usage := &billingtest.Usage{}
	e := mapgen.New(factory(okGen(), imagegen.DefaultModel, nil), nil, billingtest.Recording(usage), nil, nil)

	if _, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a fishing town"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	row := billingtest.OneRow(t, usage, campaign.TenantID, storage.ComponentLLM, "gemini", imagegen.DefaultModel)
	if row.LLMInputTokens != 120 || row.LLMOutputTokens != 1290 {
		t.Errorf("ledger tokens = %d/%d, want 120/1290", row.LLMInputTokens, row.LLMOutputTokens)
	}
}

// A platform-key Tenant with a spent allowance is refused before the generator
// is even built — the most expensive call per request in the product must not
// start. A BYOK-plan Tenant under the same gate generates.
func TestGenerate_AllowanceGate(t *testing.T) {
	t.Parallel()
	gen := okGen()
	built := 0
	countingFactory := func(context.Context, uuid.UUID) (imagegen.Generator, string, error) {
		built++
		return gen, "m", nil
	}

	usage := &billingtest.Usage{}
	e := mapgen.New(countingFactory, nil, billingtest.Exhausted(usage), nil, nil)
	if _, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a fishing town"}); !errors.Is(err, billing.ErrAllowanceExhausted) {
		t.Fatalf("err = %v, want ErrAllowanceExhausted", err)
	}
	if built != 0 || gen.calls != 0 {
		t.Fatalf("generator built %d times, called %d times; want neither on an exhausted allowance", built, gen.calls)
	}
	if n := len(usage.Rows()); n != 0 {
		t.Errorf("refused generation wrote %d ledger rows", n)
	}

	byok := mapgen.New(countingFactory, nil, billingtest.BYOK(usage), nil, nil)
	if _, err := byok.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a fishing town"}); err != nil {
		t.Fatalf("BYOK tenant: Generate = %v", err)
	}
	if gen.calls != 1 {
		t.Errorf("BYOK tenant: generator called %d times, want 1", gen.calls)
	}
}
