package mapgen_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/mapgen"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Suggested pins (#541). The whole contract is "suggest, never place": the model
// picks from entries that ALREADY EXIST and never returns a coordinate, because
// coordinates from a language model are confident and wrong, and the spatial
// layer is read by the Party Marker and both spatial Tools.

type fakeText struct {
	system string
	user   string
	reply  string
	err    error
	calls  int
}

func (f *fakeText) CallText(_ context.Context, _ storage.Campaign, _, system, user string, _ int) (string, error) {
	f.calls++
	f.system, f.user = system, user
	return f.reply, f.err
}

func candidates(names ...string) []storage.KGNode {
	out := make([]storage.KGNode, 0, len(names))
	for _, n := range names {
		out = append(out, storage.KGNode{ID: uuid.New(), Name: n, Type: storage.KGNodeLocation})
	}
	return out
}

func suggester(text *fakeText, seeds mapgen.SeedReader) *mapgen.Engine {
	return mapgen.New(factory(okGen(), "m", nil), seeds, nil, nil).WithTextCaller(text)
}

var anchorSeeds = &fakeSeeds{
	node:      storage.KGNode{Name: "Saltmarsh", Body: "A damp fishing town."},
	residents: []string{"The north gate"},
}

func TestSuggestPins_ReturnsOnlyCandidateIDs(t *testing.T) {
	t.Parallel()
	cands := candidates("The Rusty Anchor", "The docks", "The Enemy Capital")
	text := &fakeText{reply: "[1,2]"}
	e := suggester(text, anchorSeeds)

	got, err := e.SuggestPins(context.Background(), campaign, uuid.New(), cands)
	if err != nil {
		t.Fatalf("SuggestPins: %v", err)
	}
	if len(got) != 2 || got[0] != cands[0].ID || got[1] != cands[1].ID {
		t.Fatalf("suggestions = %v, want the first two candidates", got)
	}
	// The model is asked for INDICES, never uuids: a model copying 36-character
	// ids has 36 chances per entry to hallucinate a plausible-looking one.
	if strings.Contains(text.user, cands[0].ID.String()) {
		t.Error("candidate uuids were put in the prompt")
	}
	for _, n := range []string{"The Rusty Anchor", "Saltmarsh", "A damp fishing town."} {
		if !strings.Contains(text.user, n) {
			t.Errorf("prompt omits %q", n)
		}
	}
}

// TestSuggestPins_IgnoresOutOfRangeAndDuplicates: one bad number should not
// discard the good ones, and a repeat is not two pins.
func TestSuggestPins_IgnoresOutOfRangeAndDuplicates(t *testing.T) {
	t.Parallel()
	cands := candidates("A", "B")
	e := suggester(&fakeText{reply: "chatter [2, 2, 99, 0, -4, 1] more chatter"}, anchorSeeds)

	got, err := e.SuggestPins(context.Background(), campaign, uuid.New(), cands)
	if err != nil {
		t.Fatalf("SuggestPins: %v", err)
	}
	if len(got) != 2 || got[0] != cands[1].ID || got[1] != cands[0].ID {
		t.Fatalf("suggestions = %v, want B then A exactly once each", got)
	}
}

// TestSuggestPins_UnusableReplyIsNoSuggestions: "which of these belong here"
// being unanswerable means no suggestions, not an error the GM has to dismiss.
// Nothing was going to be written either way.
func TestSuggestPins_UnusableReplyIsNoSuggestions(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{"I think the tavern and the docks!", "", "[not, json]"} {
		e := suggester(&fakeText{reply: reply}, anchorSeeds)
		got, err := e.SuggestPins(context.Background(), campaign, uuid.New(), candidates("A", "B"))
		if err != nil {
			t.Fatalf("reply %q: %v", reply, err)
		}
		if len(got) != 0 {
			t.Errorf("reply %q yielded %v", reply, got)
		}
	}
}

// TestSuggestPins_EmptyArrayIsARealAnswer: the prompt says so, and treating it as
// a failure would push the model toward inventing suggestions.
func TestSuggestPins_EmptyArrayIsARealAnswer(t *testing.T) {
	t.Parallel()
	e := suggester(&fakeText{reply: "[]"}, anchorSeeds)
	got, err := e.SuggestPins(context.Background(), campaign, uuid.New(), candidates("A"))
	if err != nil {
		t.Fatalf("SuggestPins: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestSuggestPins_NoCandidatesNeverCallsTheModel(t *testing.T) {
	t.Parallel()
	text := &fakeText{reply: "[1]"}
	e := suggester(text, anchorSeeds)
	got, err := e.SuggestPins(context.Background(), campaign, uuid.New(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if text.calls != 0 {
		t.Error("an empty candidate set still spent a call")
	}
}

// TestSuggestPins_SeedsFromThePublicReadOnly: the same public-only read the image
// prompt uses. A private detail steering which public entries get suggested is a
// quieter leak than a private name in a picture, but it is the same leak.
func TestSuggestPins_SeedsFromThePublicReadOnly(t *testing.T) {
	t.Parallel()
	text := &fakeText{reply: "[1]"}
	e := suggester(text, &fakeSeeds{err: storage.ErrNotFound})

	_, err := e.SuggestPins(context.Background(), campaign, uuid.New(), candidates("A"))
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a private anchor", err)
	}
	if text.calls != 0 {
		t.Error("a private anchor still spent a call")
	}
}

// TestSuggestPins_BoundsTheCandidateList: a campaign with 800 entries must not
// put its whole wiki in a prompt.
func TestSuggestPins_BoundsTheCandidateList(t *testing.T) {
	t.Parallel()
	many := make([]storage.KGNode, 0, 800)
	for i := 0; i < 800; i++ {
		many = append(many, storage.KGNode{ID: uuid.New(), Name: "Entry", Type: storage.KGNodeLocation})
	}
	text := &fakeText{reply: "[1]"}
	e := suggester(text, anchorSeeds)

	if _, err := e.SuggestPins(context.Background(), campaign, uuid.New(), many); err != nil {
		t.Fatalf("SuggestPins: %v", err)
	}
	if n := strings.Count(text.user, "\n"); n > 200 {
		t.Errorf("prompt has %d lines; the candidate list was not bounded", n)
	}
}

// TestSuggestPins_WithoutATextProviderIsUnavailable rather than guessing.
func TestSuggestPins_WithoutATextProviderIsUnavailable(t *testing.T) {
	t.Parallel()
	e := mapgen.New(factory(okGen(), "m", nil), anchorSeeds, nil, nil)
	if _, err := e.SuggestPins(context.Background(), campaign, uuid.New(), candidates("A")); err == nil {
		t.Fatal("suggestions without a text provider should be refused")
	}
}
