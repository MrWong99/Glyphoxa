package mapgen_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/mapgen"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Generated maps (#541). Two things are load-bearing and both are asserted here:
// the call WRITES NOTHING, and the prompt it builds carries the wiki's PUBLIC
// material only.

var campaign = storage.Campaign{
	ID:       uuid.MustParse("11111111-1111-1111-1111-111111111111"),
	TenantID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
	Name:     "Saltmarsh",
	System:   "dnd5e",
}

type fakeGen struct {
	prompt string
	res    imagegen.Result
	err    error
	calls  int
}

func (f *fakeGen) Generate(_ context.Context, prompt string) (imagegen.Result, error) {
	f.calls++
	f.prompt = prompt
	return f.res, f.err
}

type fakeSeeds struct {
	node      storage.KGNode
	residents []string
	err       error
}

func (f *fakeSeeds) MapSeedContext(context.Context, uuid.UUID, uuid.UUID) (storage.KGNode, []string, error) {
	return f.node, f.residents, f.err
}

// countingRecorder proves the metering actually happened.
type countingRecorder struct {
	observe.Discard
	tokens int
	model  string
}

func (c *countingRecorder) LLMTokens(_ observe.Provider, model string, in, out int) {
	c.tokens += in + out
	c.model = model
}

func okGen() *fakeGen {
	return &fakeGen{res: imagegen.Result{
		Data: []byte{0x89, 'P', 'N', 'G'}, ContentType: "image/png",
		PromptTokens: 120, OutputTokens: 1290,
	}}
}

func factory(g imagegen.Generator, model string, err error) mapgen.GeneratorFactory {
	return func(context.Context, uuid.UUID) (imagegen.Generator, string, error) { return g, model, err }
}

func TestGenerate_ReturnsADraftAndMetersIt(t *testing.T) {
	t.Parallel()
	gen := okGen()
	rec := &countingRecorder{}
	e := mapgen.New(factory(gen, "gemini-2.5-flash-image", nil), nil, rec, nil)

	res, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a fishing town"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Data) == 0 || res.ContentType != "image/png" {
		t.Fatalf("draft came back empty: %+v", res)
	}
	if res.Model != "gemini-2.5-flash-image" {
		t.Errorf("model = %q", res.Model)
	}
	// The tokens were spent whether or not the GM keeps the picture; a discarded
	// draft that billed nothing would make the ledger lie.
	if rec.tokens != 1410 {
		t.Errorf("metered %d tokens, want 120+1290", rec.tokens)
	}
	if rec.model != "gemini-2.5-flash-image" {
		t.Errorf("metered against model %q", rec.model)
	}
}

// TestGenerate_SeedsFromTheEntrysProseAndResidents is AC2.
func TestGenerate_SeedsFromTheEntrysProseAndResidents(t *testing.T) {
	t.Parallel()
	gen := okGen()
	seeds := &fakeSeeds{
		node:      storage.KGNode{Name: "Saltmarsh", Body: "A damp fishing town on a grey estuary."},
		residents: []string{"The Rusty Anchor", "The north gate"},
	}
	e := mapgen.New(factory(gen, "m", nil), seeds, nil, nil)

	anchor := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	res, err := e.Generate(context.Background(), campaign,
		mapgen.Input{Prompt: "show the harbour", AnchorNodeID: anchor})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"Saltmarsh",                              // the anchor's name
		"A damp fishing town on a grey estuary.", // its prose
		"The Rusty Anchor", "The north gate",     // what resides in it
		"show the harbour", // and the GM's own words
	} {
		if !strings.Contains(gen.prompt, want) {
			t.Errorf("prompt omits %q:\n%s", want, gen.prompt)
		}
	}
	// The composed prompt travels back, so the GM can see what was actually asked
	// for rather than only what they typed.
	if res.Prompt != gen.prompt {
		t.Error("the returned prompt is not the one that was sent")
	}
	// No lettering: image models render labels as convincing gibberish, and a map
	// covered in nonsense words is worse than one with none.
	if !strings.Contains(strings.ToLower(gen.prompt), "no text") {
		t.Error("the prompt does not forbid lettering")
	}
}

// TestGenerate_APrivateAnchorIsNotFound: a gm_private Location has no public
// depiction to generate, and MapSeedContext's filter is what says so.
func TestGenerate_APrivateAnchorIsNotFound(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := mapgen.New(factory(gen, "m", nil), &fakeSeeds{err: storage.ErrNotFound}, nil, nil)

	_, err := e.Generate(context.Background(), campaign, mapgen.Input{
		Prompt: "anything", AnchorNodeID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if gen.calls != 0 {
		t.Error("a refused seed still spent a generation")
	}
}

// TestGenerate_TooLargeIsPermanentAndUnmetered is AC5. The provider already
// billed it; pricing zero tokens here would add a second phantom charge, and the
// error must reach the caller intact so it refuses rather than retries.
func TestGenerate_TooLargeIsPermanentAndUnmetered(t *testing.T) {
	t.Parallel()
	gen := okGen()
	gen.err = imagegen.ErrImageTooLarge
	rec := &countingRecorder{}
	e := mapgen.New(factory(gen, "m", nil), nil, rec, nil)

	_, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "an enormous continent"})
	if !errors.Is(err, imagegen.ErrImageTooLarge) {
		t.Fatalf("err = %v, want ErrImageTooLarge", err)
	}
	if rec.tokens != 0 {
		t.Errorf("a failed generation metered %d tokens", rec.tokens)
	}
}

// TestGenerate_NoKeyIsAnActionableRefusal: the GM is told to add a key, not shown
// a provider stack trace.
func TestGenerate_NoKeyIsAnActionableRefusal(t *testing.T) {
	t.Parallel()
	e := mapgen.New(factory(nil, "", mapgen.ErrNotConfigured), nil, nil, nil)
	_, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a town"})
	if !errors.Is(err, mapgen.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

// TestGenerate_EntitlementRefusalTravelsIntact: swallowing it would spend the
// deployment's key on a tenant that is not entitled to it (ADR-0054).
func TestGenerate_EntitlementRefusalTravelsIntact(t *testing.T) {
	t.Parallel()
	e := mapgen.New(factory(nil, "", llmbuild.ErrNoPlatformKeyEntitlement), nil, nil, nil)
	_, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "a town"})
	if !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
		t.Fatalf("err = %v, want the entitlement refusal", err)
	}
}

func TestGenerate_EmptyPromptIsRefusedBeforeSpending(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := mapgen.New(factory(gen, "m", nil), nil, nil, nil)
	if _, err := e.Generate(context.Background(), campaign, mapgen.Input{Prompt: "   "}); err == nil {
		t.Fatal("an empty prompt should be refused")
	}
	if gen.calls != 0 {
		t.Error("an empty prompt reached the provider")
	}
}

// TestGenerate_LongProseIsCutByRunes: a GM with a 40 000-character Location entry
// should get a map, not a rejected request — and the cut must be on a rune
// boundary or a multibyte campaign produces mojibake in its own prompt.
func TestGenerate_LongProseIsCutByRunes(t *testing.T) {
	t.Parallel()
	gen := okGen()
	seeds := &fakeSeeds{node: storage.KGNode{
		Name: "Saltmarsh",
		Body: strings.Repeat("ä", 40_000),
	}}
	e := mapgen.New(factory(gen, "m", nil), seeds, nil, nil)

	if _, err := e.Generate(context.Background(), campaign, mapgen.Input{
		Prompt: "the docks", AnchorNodeID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n := len([]rune(gen.prompt)); n > 4000 {
		t.Errorf("prompt is %d runes; the seed was not bounded", n)
	}
	if !strings.Contains(gen.prompt, "ä") || strings.Contains(gen.prompt, "\ufffd") {
		t.Error("the prose was cut mid-rune")
	}
}
