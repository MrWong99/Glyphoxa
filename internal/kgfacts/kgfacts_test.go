package kgfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeNodes is a scripted kgfacts.Nodes: it returns fixed Nodes or an error, and
// can block until ctx is cancelled to exercise the budget timeout.
type fakeNodes struct {
	nodes   []storage.KGNode
	err     error
	block   bool // block until ctx done, then return ctx.Err()
	gotCamp uuid.UUID
}

func (f *fakeNodes) ListPublicNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error) {
	f.gotCamp = campaignID
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.nodes, f.err
}

// fakeSessions is a scripted kgfacts.Sessions.
type fakeSessions struct {
	vs storage.VoiceSession
	ok bool
}

func (f fakeSessions) Snapshot() (storage.VoiceSession, bool) { return f.vs, f.ok }

// fakeMetrics records the outcomes it was handed.
type fakeMetrics struct{ outcomes []observe.FactsOutcome }

func (f *fakeMetrics) KGFacts(o observe.FactsOutcome) { f.outcomes = append(f.outcomes, o) }

func (f *fakeMetrics) count(o observe.FactsOutcome) int {
	n := 0
	for _, got := range f.outcomes {
		if got == o {
			n++
		}
	}
	return n
}

func activeSessions(campaignID uuid.UUID) fakeSessions {
	return fakeSessions{vs: storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID}, ok: true}
}

func newRecaller(t *testing.T, nodes kgfacts.Nodes, sessions kgfacts.Sessions, m kgfacts.Metrics) *kgfacts.Recaller {
	t.Helper()
	return kgfacts.New(nodes, sessions, m, nil, kgfacts.Config{})
}

// TestFacts_RenderFormatExact pins #126 AC2's rendering contract: each fact is
// "### <Name> (<TypeLabel>)\n<Body>" with the domain TypeLabel, and the recaller
// returns them in storage order.
func TestFacts_RenderFormatExact(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "The Bell", Body: "It tolls at dusk."},
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeLocation, Name: "Ravenhollow", Body: "A misty vale."},
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodePlotThread, Name: "The Heist", Body: "Someone plans a robbery."},
	}}
	m := &fakeMetrics{}
	r := newRecaller(t, nodes, activeSessions(camp), m)

	facts := r.Facts(context.Background(), "bart")
	if len(facts) != 3 {
		t.Fatalf("got %d facts, want 3", len(facts))
	}
	if facts[0] != "### The Bell (Note)\nIt tolls at dusk." {
		t.Errorf("fact[0] = %q", facts[0])
	}
	if facts[1] != "### Ravenhollow (Location)\nA misty vale." {
		t.Errorf("fact[1] = %q", facts[1])
	}
	if facts[2] != "### The Heist (Plot thread)\nSomeone plans a robbery." {
		t.Errorf("fact[2] = %q (want the 'Plot thread' TypeLabel)", facts[2])
	}
	if nodes.gotCamp != camp {
		t.Errorf("read scoped to %s, want active campaign %s", nodes.gotCamp, camp)
	}
	if m.count(observe.FactsOK) != 1 {
		t.Errorf("want one ok outcome, got %v", m.outcomes)
	}
}

// TestFacts_EmptyBodyRendersHeaderOnly pins that a bodiless Node emits just its
// header line (no dangling newline).
func TestFacts_EmptyBodyRendersHeaderOnly(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "Loose end", Body: "   "},
	}}
	r := newRecaller(t, nodes, activeSessions(camp), &fakeMetrics{})

	facts := r.Facts(context.Background(), "bart")
	if len(facts) != 1 || facts[0] != "### Loose end (Note)" {
		t.Errorf("empty-body fact = %q, want header only", facts)
	}
}

// TestFacts_TruncatesBodyByRunes pins rune-safe truncation to MaxFactChars + "…".
func TestFacts_TruncatesBodyByRunes(t *testing.T) {
	camp := uuid.New()
	body := strings.Repeat("é", kgfacts.MaxFactChars+50) // multibyte runes past the cap
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "Long", Body: body},
	}}
	r := newRecaller(t, nodes, activeSessions(camp), &fakeMetrics{})

	facts := r.Facts(context.Background(), "bart")
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	// The body portion after the header line: MaxFactChars runes + the ellipsis.
	_, bodyPart, _ := strings.Cut(facts[0], "\n")
	gotRunes := []rune(bodyPart)
	if len(gotRunes) != kgfacts.MaxFactChars+1 {
		t.Errorf("truncated body = %d runes, want %d + ellipsis", len(gotRunes), kgfacts.MaxFactChars)
	}
	if !strings.HasSuffix(bodyPart, "…") {
		t.Errorf("truncated body missing ellipsis: %q", bodyPart[len(bodyPart)-8:])
	}
}

// TestFacts_CapsAtMaxFacts pins the MaxFacts cap: a wiki larger than the cap yields
// exactly MaxFacts facts, the deterministic storage-order prefix.
func TestFacts_CapsAtMaxFacts(t *testing.T) {
	camp := uuid.New()
	var ns []storage.KGNode
	for i := 0; i < kgfacts.MaxFacts+5; i++ {
		ns = append(ns, storage.KGNode{
			ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote,
			Name: fmt.Sprintf("Node %02d", i), Body: "short",
		})
	}
	nodes := &fakeNodes{nodes: ns}
	r := newRecaller(t, nodes, activeSessions(camp), &fakeMetrics{})

	facts := r.Facts(context.Background(), "bart")
	if len(facts) != kgfacts.MaxFacts {
		t.Fatalf("got %d facts, want the MaxFacts cap %d", len(facts), kgfacts.MaxFacts)
	}
	if !strings.Contains(facts[0], "Node 00") {
		t.Errorf("first fact not the storage-order prefix: %q", facts[0])
	}
}

// TestFacts_CapsAtMaxBlockChars pins the block-budget cap: enough large facts to
// blow MaxBlockChars yields a deterministic prefix whose total stays within budget.
func TestFacts_CapsAtMaxBlockChars(t *testing.T) {
	camp := uuid.New()
	big := strings.Repeat("x", kgfacts.MaxFactChars) // each fact ~MaxFactChars body
	var ns []storage.KGNode
	// MaxBlockChars / MaxFactChars facts would just fit; add more to force the stop.
	for i := 0; i < (kgfacts.MaxBlockChars/kgfacts.MaxFactChars)+5; i++ {
		ns = append(ns, storage.KGNode{
			ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote,
			Name: fmt.Sprintf("N%02d", i), Body: big,
		})
	}
	nodes := &fakeNodes{nodes: ns}
	r := newRecaller(t, nodes, activeSessions(camp), &fakeMetrics{})

	facts := r.Facts(context.Background(), "bart")
	if len(facts) == 0 {
		t.Fatal("no facts returned")
	}
	if len(facts) >= len(ns) {
		t.Errorf("block cap did not trim: got %d of %d facts", len(facts), len(ns))
	}
	// Total block length (header + "\n\n"-joined facts) stays within MaxBlockChars.
	total := len("## What you know about the world") + len("\n\n") + len(strings.Join(facts, "\n\n"))
	if total > kgfacts.MaxBlockChars {
		t.Errorf("assembled block = %d bytes, exceeds MaxBlockChars %d", total, kgfacts.MaxBlockChars)
	}
}

// TestFacts_NoSession_Nil pins that with no active session there is nothing to
// scope, so no facts are returned.
func TestFacts_NoSession_Nil(t *testing.T) {
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{}, fakeSessions{ok: false}, m)

	if facts := r.Facts(context.Background(), "bart"); facts != nil {
		t.Errorf("facts with no session = %v, want nil", facts)
	}
	if m.count(observe.FactsDegraded) != 0 {
		t.Errorf("no-session must not count a degraded read: %v", m.outcomes)
	}
}

// TestFacts_NoPublicNodes_Empty pins that a read finding no public Nodes returns
// nil and counts an empty outcome (not degraded).
func TestFacts_NoPublicNodes_Empty(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{nodes: nil}, activeSessions(camp), m)

	if facts := r.Facts(context.Background(), "bart"); facts != nil {
		t.Errorf("facts = %v, want nil", facts)
	}
	if m.count(observe.FactsEmpty) != 1 || m.count(observe.FactsDegraded) != 0 {
		t.Errorf("want one empty, zero degraded: %v", m.outcomes)
	}
}

// TestFacts_DBError_DegradedNil pins that a DB failure degrades to nil and counts
// a degraded read.
func TestFacts_DBError_DegradedNil(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{err: errors.New("db down")}, activeSessions(camp), m)

	if facts := r.Facts(context.Background(), "bart"); facts != nil {
		t.Errorf("facts on DB error = %v, want nil", facts)
	}
	if m.count(observe.FactsDegraded) != 1 {
		t.Errorf("DB error must count one degraded read: %v", m.outcomes)
	}
}

// TestFacts_CtxCancel_SilentNil pins the barge posture: a cancelled turn ctx yields
// nil and counts NOTHING (the turn is gone, nothing wasted — mirrors recall).
func TestFacts_CtxCancel_SilentNil(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{}, activeSessions(camp), m)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // barge before the read starts

	if facts := r.Facts(ctx, "bart"); facts != nil {
		t.Errorf("facts on cancelled ctx = %v, want nil", facts)
	}
	if len(m.outcomes) != 0 {
		t.Errorf("a barge cancel must count nothing: %v", m.outcomes)
	}
}

// TestFacts_BudgetTimeout_DegradedNil pins the hard budget: a read that overruns
// the budget degrades to nil and counts a degraded read.
func TestFacts_BudgetTimeout_DegradedNil(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := kgfacts.New(&fakeNodes{block: true}, activeSessions(camp), m, nil,
		kgfacts.Config{Budget: 20 * time.Millisecond})

	start := time.Now()
	facts := r.Facts(context.Background(), "bart")
	if facts != nil {
		t.Errorf("facts on budget timeout = %v, want nil", facts)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Facts blocked %v past its budget — the hard timeout did not fire", elapsed)
	}
	if m.count(observe.FactsDegraded) != 1 {
		t.Errorf("budget timeout must count one degraded read: %v", m.outcomes)
	}
}
