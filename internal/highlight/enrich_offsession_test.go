package highlight

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
)

// Highlight enrichment is the off-session paid call with no request behind it
// (#658): the jobs run from the queue. These tests pin the two halves of the
// rule for both jobs — the spend reaches the Tenant's Usage Ledger, and an
// exhausted plan allowance SKIPS the paid step cleanly instead of spending or
// retrying.

func okImageGen() *fakeGen {
	return &fakeGen{res: imagegen.Result{Data: []byte("PNGDATA"), ContentType: "image/png", PromptTokens: 40, OutputTokens: 1290}}
}

// A generated Highlight image — the most expensive call per request in the
// product — is ledgered for the Highlight's Tenant under the image model.
func TestEnrichImageHandler_ReachesTheUsageLedger(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	usage := &billingtest.Usage{}

	handler := EnrichImageHandler(store, newFakeBlobs(), factoryReturning(okImageGen(), imagegen.DefaultModel, nil),
		billingtest.Recording(usage), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}

	row := billingtest.OneRow(t, usage, tenantID, storage.ComponentLLM, "gemini", imagegen.DefaultModel)
	if row.LLMInputTokens != 40 || row.LLMOutputTokens != 1290 {
		t.Errorf("ledger tokens = %d/%d, want 40/1290", row.LLMInputTokens, row.LLMOutputTokens)
	}
}

// The generation is billed whether or not the image lands: a blob failure
// after the provider call returns an error (the job retries) but the usage is
// already in the ledger.
func TestEnrichImageHandler_LedgersEvenWhenTheImageFailsToLand(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	blobs.putErr = errors.New("disk full")
	usage := &billingtest.Usage{}

	handler := EnrichImageHandler(store, blobs, factoryReturning(okImageGen(), imagegen.DefaultModel, nil),
		billingtest.Recording(usage), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err == nil {
		t.Fatal("handler = nil, want the blob error so the job retries")
	}
	billingtest.OneRow(t, usage, tenantID, storage.ComponentLLM, "gemini", imagegen.DefaultModel)
}

// An exhausted allowance skips the paid step: no generator built, no provider
// call, no claim left behind, the Highlight untouched — and the job reports
// DONE (nil), so the runner does not retry a refusal until the month turns.
func TestEnrichImageHandler_ExhaustedAllowanceSkipsCleanly(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	blobs := newFakeBlobs()
	gen := okImageGen()
	built := 0
	factory := func(context.Context, uuid.UUID) (imagegen.Generator, string, error) {
		built++
		return gen, "m", nil
	}
	usage := &billingtest.Usage{}

	handler := EnrichImageHandler(store, blobs, factory, billingtest.Exhausted(usage), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler = %v, want nil (a clean skip, not a retry)", err)
	}
	if built != 0 || gen.calls != 0 {
		t.Fatalf("generator built %d times, called %d times; want neither", built, gen.calls)
	}
	if got := store.rows[h.ID]; got.ImageKey != "" || got.ClipKey != h.ClipKey {
		t.Errorf("highlight mutated by a skipped enrichment: %+v", got)
	}
	if len(store.claimed) != 0 {
		t.Errorf("skip left %d enrichment claims held", len(store.claimed))
	}
	if n := len(usage.Rows()); n != 0 {
		t.Errorf("skipped enrichment wrote %d ledger rows", n)
	}
}

// A BYOK-plan Tenant under the same gate is enriched.
func TestEnrichImageHandler_BYOKTenantIsNotGated(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	gen := okImageGen()

	handler := EnrichImageHandler(store, newFakeBlobs(), factoryReturning(gen, "m", nil),
		billingtest.BYOK(&billingtest.Usage{}), nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gen.calls != 1 || store.rows[h.ID].ImageKey == "" {
		t.Errorf("BYOK tenant: generator calls = %d, image key = %q; want an enriched highlight", gen.calls, store.rows[h.ID].ImageKey)
	}
}

// An allowance that cannot be READ is not an exhausted one: the job returns
// the error so it retries, rather than spending ungated or skipping for good.
func TestEnrichImageHandler_AllowanceReadErrorRetries(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeEnrichStore()
	h := seedRow(store, tenantID)
	gen := okImageGen()
	seam := billing.NewOffSession(&billingtest.Usage{}, failingAllowance{}, nil)

	handler := EnrichImageHandler(store, newFakeBlobs(), factoryReturning(gen, "m", nil), seam, nil, nil)
	payload, _ := MarshalEnrichImage(h.ID, tenantID)
	if err := handler(context.Background(), payload); err == nil {
		t.Fatal("handler = nil, want an error so the job retries")
	}
	if gen.calls != 0 {
		t.Errorf("generator called %d times with the gate unreadable", gen.calls)
	}
}

type failingAllowance struct{}

func (failingAllowance) AllowanceState(context.Context, uuid.UUID) (spend.AllowanceState, error) {
	return spend.AllowanceState{}, errors.New("db down")
}

func okSoundGen() *fakeSoundGen {
	return &fakeSoundGen{res: soundgen.Result{
		Data: []byte("mp3"), ContentType: "audio/mpeg", Model: "eleven_text_to_sound_v2", CharacterCost: 100,
	}}
}

// Sound enrichment under an exhausted allowance: the same clean skip.
func TestEnrichSoundHandler_ExhaustedAllowanceSkipsCleanly(t *testing.T) {
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)
	gen := okSoundGen()
	built := 0
	factory := func(context.Context, uuid.UUID) (soundgen.Generator, error) {
		built++
		return gen, nil
	}
	usage := &billingtest.Usage{}

	h := EnrichSoundHandler(store, newFakeBlobs(), factory, billingtest.Exhausted(usage), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
		t.Fatalf("handler = %v, want nil (a clean skip, not a retry)", err)
	}
	if built != 0 || gen.genCalls != 0 {
		t.Fatalf("generator built %d times, called %d times; want neither", built, gen.genCalls)
	}
	if store.rows[id].SoundKey != "" {
		t.Errorf("skipped enrichment landed a sound: %+v", store.rows[id])
	}
	if n := len(usage.Rows()); n != 0 {
		t.Errorf("skipped enrichment wrote %d ledger rows", n)
	}
}

// A BYOK-plan Tenant under the same gate gets its sound, ledgered for the
// right Tenant under the tts component with the SFX model id.
func TestEnrichSoundHandler_BYOKTenantIsNotGatedAndIsLedgered(t *testing.T) {
	tenantID, id := uuid.New(), uuid.New()
	store := newFakeSoundEnrichStore()
	store.rows[id] = soundRow(tenantID, id, storage.SoundKindSting)
	gen := okSoundGen()
	usage := &billingtest.Usage{}

	h := EnrichSoundHandler(store, newFakeBlobs(), soundFactoryOf(gen, nil), billingtest.BYOK(usage), nil)
	if err := h(context.Background(), soundPayload(t, id, tenantID, storage.SoundKindSting)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gen.genCalls != 1 {
		t.Fatalf("generator called %d times, want 1", gen.genCalls)
	}
	row := billingtest.OneRow(t, usage, tenantID, storage.ComponentTTS, "elevenlabs", "eleven_text_to_sound_v2")
	if row.TTSCharacters != 100 {
		t.Errorf("ledger character cost = %d, want 100", row.TTSCharacters)
	}
}
