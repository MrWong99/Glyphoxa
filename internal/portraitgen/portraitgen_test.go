package portraitgen_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/portraitgen"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Node portraits (#590). Two things are load-bearing and both are asserted
// here, exactly as in mapgen's suite: the call WRITES NOTHING, and the prompt
// it builds carries the entry's PUBLIC material only (the seed read's filter is
// what says so).

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
	node storage.KGNode
	err  error
}

func (f *fakeSeeds) PortraitSeedContext(context.Context, uuid.UUID, uuid.UUID) (storage.KGNode, error) {
	return f.node, f.err
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

func factory(g imagegen.Generator, model string, err error) portraitgen.GeneratorFactory {
	return func(context.Context, uuid.UUID) (imagegen.Generator, string, error) { return g, model, err }
}

func bart() storage.KGNode {
	return storage.KGNode{
		ID:   uuid.New(),
		Type: storage.KGNodeNPC,
		Name: "Bart",
		Body: "The innkeeper of the Rusty Anchor, always owed money.",
		Aspects: []storage.KGNodeAspect{
			{Key: "appearance", Value: "red beard, one eye"},
			{Key: "demeanour", Value: "gruff but fair"},
		},
	}
}

func TestGenerate_ReturnsADraftAndMetersIt(t *testing.T) {
	t.Parallel()
	gen := okGen()
	rec := &countingRecorder{}
	e := portraitgen.New(factory(gen, "gemini-2.5-flash-image", nil), &fakeSeeds{node: bart()}, rec, nil)

	res, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()})
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

// TestGenerate_SeedsFromTheEntrysProseAndFacts: the entry's name, prose and
// public facts all reach the prompt, plus the GM's own words when given — and
// the composed prompt travels back so the GM can see what was actually asked.
func TestGenerate_SeedsFromTheEntrysProseAndFacts(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: bart()}, nil, nil)

	res, err := e.Generate(context.Background(), campaign,
		portraitgen.Input{NodeID: uuid.New(), Prompt: "mid-laugh, holding a tankard"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"Bart",                              // the entry's name
		"The innkeeper of the Rusty Anchor", // its prose
		"appearance: red beard, one eye",    // its public facts
		"demeanour: gruff but fair",
		"mid-laugh, holding a tankard", // and the GM's own words
		"Saltmarsh",                    // the campaign framing
	} {
		if !strings.Contains(gen.prompt, want) {
			t.Errorf("prompt omits %q:\n%s", want, gen.prompt)
		}
	}
	if res.Prompt != gen.prompt {
		t.Error("the returned prompt is not the one that was sent")
	}
	// No lettering: image models render nameplates as convincing gibberish.
	if !strings.Contains(strings.ToLower(gen.prompt), "no text") {
		t.Error("the prompt does not forbid lettering")
	}
}

// TestGenerate_AnEmptyGMPromptIsFine: unlike a map, the entry's own prose IS
// the prompt — the GM pressing "generate" with no extra words must work.
func TestGenerate_AnEmptyGMPromptIsFine(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: bart()}, nil, nil)

	if _, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()}); err != nil {
		t.Fatalf("Generate with no GM prompt: %v", err)
	}
	if strings.Contains(gen.prompt, "The GM asks for") {
		t.Error("an empty GM prompt still rendered its clause")
	}
}

// TestGenerate_APrivateEntryIsNotFound: a gm_private entry has no public
// depiction to generate, and PortraitSeedContext's filter is what says so.
func TestGenerate_APrivateEntryIsNotFound(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{err: storage.ErrNotFound}, nil, nil)

	_, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if gen.calls != 0 {
		t.Error("a refused seed still spent a generation")
	}
}

// TestGenerate_TooLargeIsPermanentAndUnmetered: the provider already billed it;
// pricing zero tokens here would add a second phantom charge, and the error
// must reach the caller intact so it refuses rather than retries.
func TestGenerate_TooLargeIsPermanentAndUnmetered(t *testing.T) {
	t.Parallel()
	gen := okGen()
	gen.err = imagegen.ErrImageTooLarge
	rec := &countingRecorder{}
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: bart()}, rec, nil)

	_, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()})
	if !errors.Is(err, imagegen.ErrImageTooLarge) {
		t.Fatalf("err = %v, want ErrImageTooLarge", err)
	}
	if rec.tokens != 0 {
		t.Errorf("a failed generation metered %d tokens", rec.tokens)
	}
}

// TestGenerate_NoKeyIsAnActionableRefusal: the GM is told to add a key, not
// shown a provider stack trace.
func TestGenerate_NoKeyIsAnActionableRefusal(t *testing.T) {
	t.Parallel()
	e := portraitgen.New(factory(nil, "", portraitgen.ErrNotConfigured), &fakeSeeds{node: bart()}, nil, nil)
	_, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()})
	if !errors.Is(err, portraitgen.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

// TestGenerate_EntitlementRefusalTravelsIntact: swallowing it would spend the
// deployment's key on a tenant that is not entitled to it (ADR-0054).
func TestGenerate_EntitlementRefusalTravelsIntact(t *testing.T) {
	t.Parallel()
	e := portraitgen.New(factory(nil, "", llmbuild.ErrNoPlatformKeyEntitlement), &fakeSeeds{node: bart()}, nil, nil)
	_, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()})
	if !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
		t.Fatalf("err = %v, want the entitlement refusal", err)
	}
}

// TestGenerate_LongProseIsCutByRunes: a GM with a 40 000-character biography
// should get a portrait, not a rejected request — and the cut must be on a rune
// boundary or a multibyte campaign produces mojibake in its own prompt.
func TestGenerate_LongProseIsCutByRunes(t *testing.T) {
	t.Parallel()
	gen := okGen()
	node := bart()
	node.Body = strings.Repeat("ä", 40_000)
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: node}, nil, nil)

	if _, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n := len([]rune(gen.prompt)); n > 4000 {
		t.Errorf("prompt is %d runes; the seed was not bounded", n)
	}
	if !strings.Contains(gen.prompt, "ä") || strings.Contains(gen.prompt, "\ufffd") {
		t.Error("the prose was cut mid-rune")
	}
}

// TestGenerate_AspectFloodIsBounded: a hundred facts shape nothing — the prompt
// folds in a bounded prefix rather than the whole ledger.
func TestGenerate_AspectFloodIsBounded(t *testing.T) {
	t.Parallel()
	gen := okGen()
	node := bart()
	node.Aspects = nil
	for i := 0; i < 100; i++ {
		node.Aspects = append(node.Aspects, storage.KGNodeAspect{Key: "k", Value: strings.Repeat("v", 20)})
	}
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: node}, nil, nil)

	if _, err := e.Generate(context.Background(), campaign, portraitgen.Input{NodeID: uuid.New()}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := strings.Count(gen.prompt, "k: "); got > 12 {
		t.Errorf("%d facts reached the prompt; want at most 12", got)
	}
}

// TestGenerate_ANilNodeIDIsRefusedBeforeSpending: a portrait is OF something
// the wiki knows; there is no bare-prompt mode to fall back to.
func TestGenerate_ANilNodeIDIsRefusedBeforeSpending(t *testing.T) {
	t.Parallel()
	gen := okGen()
	e := portraitgen.New(factory(gen, "m", nil), &fakeSeeds{node: bart()}, nil, nil)
	if _, err := e.Generate(context.Background(), campaign, portraitgen.Input{}); err == nil {
		t.Fatal("a nil node id should be refused")
	}
	if gen.calls != 0 {
		t.Error("a refused input reached the provider")
	}
}
