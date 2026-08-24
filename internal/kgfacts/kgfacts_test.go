package kgfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// testAgent is a valid Agent uuid used across the unit tests — Facts now parses the
// agentID string, so the render/cap tests must pass a real uuid to reach the read.
var testAgent = uuid.New()

// fakeNodes is a scripted kgfacts.PromptKG: it returns fixed Nodes or an error, and
// can block until ctx is cancelled to exercise the budget timeout.
type fakeNodes struct {
	nodes    []storage.KGNode
	err      error
	block    bool // block until ctx done, then return ctx.Err()
	gotAgent uuid.UUID
}

func (f *fakeNodes) AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error) {
	f.gotAgent = agentID
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.nodes, f.err
}

// liveCtx builds a run context carrying the session Identity the ctx-scoped Facts
// guard reads (#487) — the context a manager-run turn descends from.
func liveCtx(campaignID uuid.UUID) context.Context {
	return session.NewContext(context.Background(), session.Identity{CampaignID: campaignID})
}

// fakeMetrics records the outcomes it was handed, with their durations (#604).
type fakeMetrics struct {
	outcomes []observe.FactsOutcome
	durs     []time.Duration
}

func (f *fakeMetrics) KGFacts(o observe.FactsOutcome, d time.Duration) {
	f.outcomes = append(f.outcomes, o)
	f.durs = append(f.durs, d)
}

// durations returns the durations recorded for one outcome, in order (#604).
func (f *fakeMetrics) durations(o observe.FactsOutcome) []time.Duration {
	var out []time.Duration
	for i, got := range f.outcomes {
		if got == o {
			out = append(out, f.durs[i])
		}
	}
	return out
}

func (f *fakeMetrics) count(o observe.FactsOutcome) int {
	n := 0
	for _, got := range f.outcomes {
		if got == o {
			n++
		}
	}
	return n
}

func newRecaller(t *testing.T, nodes kgfacts.PromptKG, m kgfacts.Metrics) *kgfacts.Recaller {
	t.Helper()
	return kgfacts.New(nodes, m, nil, kgfacts.Config{})
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
	r := newRecaller(t, nodes, m)

	facts := r.Facts(liveCtx(camp), testAgent.String())
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
	if nodes.gotAgent != testAgent {
		t.Errorf("read scoped to agent %s, want the parsed Facts agent %s", nodes.gotAgent, testAgent)
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
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
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
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
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

// TestFacts_TruncatesLongName pins the PR #202 finding: a pathologically long
// Node name is rune-capped so it can't single-handedly blow MaxBlockChars. Since
// the read is newest-first, an un-capped huge name would land first and the
// deterministic prefix-stop would drop the whole block; here the second Node must
// still surface.
func TestFacts_TruncatesLongName(t *testing.T) {
	camp := uuid.New()
	huge := strings.Repeat("A", 5000) // one name larger than MaxBlockChars
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: huge, Body: "short"},
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "Small", Body: "also short"},
	}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2 (a long name must not kill the block)", len(facts))
	}
	head, _, _ := strings.Cut(facts[0], "\n")
	if !strings.Contains(head, "…") {
		t.Errorf("long name not truncated: %.40q", head)
	}
	// The header is bounded: name (≤ MaxNameChars + ellipsis) + "### " + " (Note)".
	if got := len([]rune(head)); got > kgfacts.MaxNameChars+20 {
		t.Errorf("header still oversized: %d runes", got)
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
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
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
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
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
	r := newRecaller(t, &fakeNodes{}, m)

	if facts := r.Facts(context.Background(), testAgent.String()); facts != nil {
		t.Errorf("facts with no session = %v, want nil", facts)
	}
	if m.count(observe.FactsDegraded) != 0 {
		t.Errorf("no-session must not count a degraded read: %v", m.outcomes)
	}
}

// TestFacts_RecordsDurationWithOutcome pins the budget-headroom signal (#604):
// each of the three outcomes carries how long the read took, so a degraded read
// can be told apart as a 50ms budget overrun rather than an instant DB refusal.
func TestFacts_RecordsDurationWithOutcome(t *testing.T) {
	camp := uuid.New()

	cases := []struct {
		name  string
		nodes *fakeNodes
		want  observe.FactsOutcome
	}{
		{"ok", &fakeNodes{nodes: []storage.KGNode{{ID: uuid.New(), Name: "Bart", Body: "Keeps the tavern."}}}, observe.FactsOK},
		{"empty", &fakeNodes{}, observe.FactsEmpty},
		{"degraded", &fakeNodes{err: errors.New("db down")}, observe.FactsDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMetrics{}
			r := newRecaller(t, tc.nodes, m)

			r.Facts(liveCtx(camp), testAgent.String())

			durs := m.durations(tc.want)
			if len(durs) != 1 {
				t.Fatalf("%s durations = %v (outcomes %v), want exactly one", tc.want, durs, m.outcomes)
			}
			if durs[0] <= 0 {
				t.Errorf("%s duration = %v, want the elapsed read time (> 0)", tc.want, durs[0])
			}
		})
	}
}

// TestFacts_UnlinkedAgent_Empty pins the #133 behaviour change: an Agent whose
// neighbourhood read is empty (an UNLINKED Character NPC — no linked Node, so no
// own facts and no neighbours) gets an EMPTY facts slot, counted empty (not
// degraded). There is deliberately no campaign-wide fallback.
func TestFacts_UnlinkedAgent_Empty(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{nodes: nil}, m)

	if facts := r.Facts(liveCtx(camp), testAgent.String()); facts != nil {
		t.Errorf("facts = %v, want nil", facts)
	}
	if m.count(observe.FactsEmpty) != 1 || m.count(observe.FactsDegraded) != 0 {
		t.Errorf("want one empty, zero degraded: %v", m.outcomes)
	}
}

// TestFacts_InvalidAgentID_Empty pins that an unparseable or empty agentID string
// (a Persona with no persisted uuid) yields no facts, counted empty (not degraded),
// and never reaches the storage read.
func TestFacts_InvalidAgentID_Empty(t *testing.T) {
	camp := uuid.New()
	for _, bad := range []string{"", "not-a-uuid", "bart"} {
		m := &fakeMetrics{}
		nodes := &fakeNodes{nodes: []storage.KGNode{
			{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "Unreached", Body: "x"},
		}}
		r := newRecaller(t, nodes, m)

		if facts := r.Facts(liveCtx(camp), bad); facts != nil {
			t.Errorf("facts for agentID %q = %v, want nil", bad, facts)
		}
		if nodes.gotAgent != uuid.Nil {
			t.Errorf("agentID %q reached the storage read as %s", bad, nodes.gotAgent)
		}
		if m.count(observe.FactsEmpty) != 1 || m.count(observe.FactsDegraded) != 0 {
			t.Errorf("agentID %q: want one empty, zero degraded: %v", bad, m.outcomes)
		}
	}
}

// TestFacts_DBError_DegradedNil pins that a DB failure degrades to nil and counts
// a degraded read.
func TestFacts_DBError_DegradedNil(t *testing.T) {
	camp := uuid.New()
	m := &fakeMetrics{}
	r := newRecaller(t, &fakeNodes{err: errors.New("db down")}, m)

	if facts := r.Facts(liveCtx(camp), testAgent.String()); facts != nil {
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
	r := newRecaller(t, &fakeNodes{}, m)

	ctx, cancel := context.WithCancel(liveCtx(camp))
	cancel() // barge before the read starts

	if facts := r.Facts(ctx, testAgent.String()); facts != nil {
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
	r := kgfacts.New(&fakeNodes{block: true}, m, nil,
		kgfacts.Config{Budget: 20 * time.Millisecond})

	start := time.Now()
	facts := r.Facts(liveCtx(camp), testAgent.String())
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

// TestFacts_RendersPublicAspects pins the #542 rendering contract: a Node's
// public Aspects render as "- Key: Value" lines between the header and the
// free-form body remainder, in their stored order.
func TestFacts_RendersPublicAspects(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Bart",
		Body: "Wipes the same glass all evening.",
		Aspects: []storage.KGNodeAspect{
			{Position: 0, Key: "Role", Value: "Runs the Rusty Anchor"},
			{Position: 1, Key: "Manner", Value: "Grumbles about the harbourmaster"},
		},
	}}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	want := "### Bart (NPC)\n" +
		"- Role: Runs the Rusty Anchor\n" +
		"- Manner: Grumbles about the harbourmaster\n\n" +
		"Wipes the same glass all evening."
	if len(facts) != 1 || facts[0] != want {
		t.Errorf("fact = %q,\nwant %q", facts, want)
	}
}

// TestFacts_SkipsPrivateAspects is the renderer half of the per-Aspect privacy
// guarantee (#542). The storage seam already filters gm_private Aspects in SQL —
// this pins that the renderer ALSO refuses to emit one, so a future read that
// forgets the flag cannot quietly ship a GM secret into a prompt.
func TestFacts_SkipsPrivateAspects(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Bart",
		Aspects: []storage.KGNodeAspect{
			{Position: 0, Key: "Role", Value: "Runs the Rusty Anchor"},
			{Position: 1, Key: "Secret", Value: "Took the smugglers' bribe", GMPrivate: true},
		},
	}}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	if strings.Contains(facts[0], "bribe") || strings.Contains(facts[0], "Secret") {
		t.Errorf("private aspect reached the prompt: %q", facts[0])
	}
	if !strings.Contains(facts[0], "Runs the Rusty Anchor") {
		t.Errorf("public aspect missing — the node lost its public facts with its secret: %q", facts[0])
	}
}

// TestFacts_AspectsRespectFactBudget pins #542's budget AC: a Node carrying many
// long Aspects is truncated to the SAME per-fact bound a long body is, so aspects
// cannot expand one entry past MaxFactChars and blow the block budget.
func TestFacts_AspectsRespectFactBudget(t *testing.T) {
	camp := uuid.New()
	var aspects []storage.KGNodeAspect
	for i := range 40 {
		aspects = append(aspects, storage.KGNodeAspect{
			Position: i, Key: "Fact", Value: strings.Repeat("é", 200),
		})
	}
	nodes := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "Overstuffed",
		Body: "and a body too", Aspects: aspects,
	}}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	// TWO-SIDED on purpose. An upper bound alone passes for a renderer that emits
	// ten runes of an eight-thousand-rune Node — the budget is a target as much as
	// a ceiling, and this is the suite's only multibyte-rune coverage, so an
	// off-by-one in the rune arithmetic has to show up here. The ±1 band absorbs
	// the ellipsis accounting: a split fits each side exactly, a single source
	// keeps the ellipsis-appending truncation.
	_, content, _ := strings.Cut(facts[0], "\n")
	if got := len([]rune(content)); got < kgfacts.MaxFactChars-1 || got > kgfacts.MaxFactChars+1 {
		t.Errorf("composed content = %d runes, want the budget %d filled (±1)", got, kgfacts.MaxFactChars)
	}
	if len(facts[0]) > kgfacts.MaxBlockChars {
		t.Errorf("one fact = %d bytes, past the whole block budget %d", len(facts[0]), kgfacts.MaxBlockChars)
	}
}

// TestFacts_NoAspectsRendersUnchanged pins the compatibility half of #542: an
// entry with no Aspects renders byte-identically to before the slice, so the
// retained free-form body is genuinely unaffected.
func TestFacts_NoAspectsRendersUnchanged(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "The Bell", Body: "It tolls at dusk."},
	}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 1 || facts[0] != "### The Bell (Note)\nIt tolls at dusk." {
		t.Errorf("fact = %q, want the pre-aspect rendering unchanged", facts)
	}
}

// TestRenderPreview_MatchesInjectedFacts pins the load-bearing property of the
// #535 lens: the preview is the SAME render the voice loop injects. If these two
// could differ, the lens would confidently show a GM something their NPC never
// receives — worse than showing nothing.
func TestRenderPreview_MatchesInjectedFacts(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Bart", Body: "An innkeeper.",
			Aspects: []storage.KGNodeAspect{{Key: "Role", Value: "Runs the Rusty Anchor"}}},
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeLocation, Name: "Saltmarsh", Body: "A damp town."},
	}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	injected := r.Facts(liveCtx(camp), testAgent.String())
	preview := kgfacts.RenderPreview(nodes.nodes)
	if len(preview.Facts) != len(injected) {
		t.Fatalf("preview has %d facts, injected %d", len(preview.Facts), len(injected))
	}
	for i := range injected {
		if preview.Facts[i] != injected[i] {
			t.Errorf("fact[%d] preview %q != injected %q", i, preview.Facts[i], injected[i])
		}
	}
	if len(preview.IncludedIDs) != len(nodes.nodes) || preview.Truncated {
		t.Errorf("preview = %+v, want everything included and untruncated", preview)
	}
	if preview.MaxChars != kgfacts.MaxBlockChars || preview.MaxFacts != kgfacts.MaxFacts {
		t.Errorf("preview budget = %d/%d, want the real caps", preview.MaxChars, preview.MaxFacts)
	}
	if preview.Chars <= 0 || preview.Chars > kgfacts.MaxBlockChars {
		t.Errorf("preview.Chars = %d, want a real consumption inside the budget", preview.Chars)
	}
}

// TestRenderPreview_ReportsPrefixStop pins the truncation reporting: the cut is a
// PREFIX-stop, so once a Node overruns, every later Node is dropped too — a
// smaller one behind it must never sneak in. The lens exists to make that cut
// visible, so it must report exactly which Nodes lost.
func TestRenderPreview_ReportsPrefixStop(t *testing.T) {
	big := storage.KGNode{
		ID: uuid.New(), Type: storage.KGNodeNote, Name: "Huge",
		Body: strings.Repeat("x", kgfacts.MaxFactChars),
	}
	nodes := []storage.KGNode{}
	for range 12 { // 12 × ~500 chars overruns the 4000-char block
		n := big
		n.ID = uuid.New()
		nodes = append(nodes, n)
	}
	// A tiny Node placed AFTER the overrun; the prefix-stop must drop it too.
	tiny := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNote, Name: "Tiny"}
	nodes = append(nodes, tiny)

	p := kgfacts.RenderPreview(nodes)
	if !p.Truncated {
		t.Fatal("preview did not report truncation on an over-budget node set")
	}
	if len(p.DroppedIDs) == 0 {
		t.Fatal("truncated preview named no dropped nodes")
	}
	for _, id := range p.IncludedIDs {
		if id == tiny.ID {
			t.Error("a small node AFTER the prefix-stop was included — the cut must be a prefix, not a skip-scan")
		}
	}
	if len(p.IncludedIDs)+len(p.DroppedIDs) != len(nodes) {
		t.Errorf("preview accounted for %d+%d of %d nodes; every node must be in exactly one bucket",
			len(p.IncludedIDs), len(p.DroppedIDs), len(nodes))
	}
	if p.Chars > kgfacts.MaxBlockChars {
		t.Errorf("preview.Chars = %d, past the budget %d", p.Chars, kgfacts.MaxBlockChars)
	}
}

// TestRenderPreview_MaxFactsCap pins the count cap's reporting half.
func TestRenderPreview_MaxFactsCap(t *testing.T) {
	var nodes []storage.KGNode
	for i := range kgfacts.MaxFacts + 5 {
		nodes = append(nodes, storage.KGNode{
			ID: uuid.New(), Type: storage.KGNodeNote, Name: "N" + strconv.Itoa(i),
		})
	}
	p := kgfacts.RenderPreview(nodes)
	if len(p.Facts) != kgfacts.MaxFacts {
		t.Errorf("got %d facts, want the cap %d", len(p.Facts), kgfacts.MaxFacts)
	}
	if !p.Truncated || len(p.DroppedIDs) != 5 {
		t.Errorf("preview = truncated %v with %d dropped, want 5 dropped", p.Truncated, len(p.DroppedIDs))
	}
}

// TestRenderPreview_Empty pins that an unlinked Agent's empty read is an empty
// preview, not a truncated one — "nothing to say" and "too much to say" are
// different problems and the lens must not confuse them.
func TestRenderPreview_Empty(t *testing.T) {
	p := kgfacts.RenderPreview(nil)
	if len(p.Facts) != 0 || p.Truncated || p.Chars != 0 {
		t.Errorf("empty preview = %+v, want zero facts, untruncated, zero chars", p)
	}
}

// TestRenderPreview_ContentFacts pins the distinction the roster dashboard
// depends on. A linked NPC's own Node is ALWAYS returned at hop 0 and always
// renders at least its header line, so "does this NPC have facts" measured as
// len(Facts) can never fail — for exactly the NPC the dashboard exists to catch.
func TestRenderPreview_ContentFacts(t *testing.T) {
	empty := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart"}
	full := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeLocation, Name: "Saltmarsh", Body: "A damp town."}

	only := kgfacts.RenderPreview([]storage.KGNode{empty})
	if len(only.Facts) != 1 {
		t.Fatalf("an empty node still renders its header: got %d facts", len(only.Facts))
	}
	if only.ContentFacts != 0 {
		t.Errorf("ContentFacts = %d for a header-only block, want 0", only.ContentFacts)
	}

	both := kgfacts.RenderPreview([]storage.KGNode{empty, full})
	if both.ContentFacts != 1 {
		t.Errorf("ContentFacts = %d, want only the one that says something", both.ContentFacts)
	}
	if len(both.Facts) != 2 {
		t.Errorf("Facts = %d, want both rendered (the block is what it is)", len(both.Facts))
	}
}

// TestFacts_AspectsDoNotEvictTheBody is the regression pin for a real quality
// bug: Aspects are composed BEFORE the body, so truncating the composite from the
// end deleted the GM's authored prose wholesale for any Node carrying MaxFactChars
// of aspects — silently, with no signal anywhere.
//
// A GM who wrote both meant both to reach the table, so each side is cut instead.
func TestFacts_AspectsDoNotEvictTheBody(t *testing.T) {
	camp := uuid.New()
	var aspects []storage.KGNodeAspect
	for i := range 30 {
		aspects = append(aspects, storage.KGNodeAspect{
			Position: i, Key: "Fact", Value: strings.Repeat("a", 60),
		})
	}
	nodes := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Overstuffed",
		Body:    "THE BODY SURVIVES.",
		Aspects: aspects,
	}}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	if !strings.Contains(facts[0], "THE BODY SURVIVES.") {
		t.Error("the authored body was evicted by the aspects")
	}
	if !strings.Contains(facts[0], "Fact:") {
		t.Error("the aspects were evicted by the body")
	}
	// The whole fact respects the per-fact bound AND fills it — a split that
	// silently threw away half the budget would satisfy a ceiling-only assertion.
	_, content, _ := strings.Cut(facts[0], "\n")
	if got := len([]rune(content)); got < kgfacts.MaxFactChars-1 || got > kgfacts.MaxFactChars+1 {
		t.Errorf("composed content = %d runes, want the budget %d filled (±1)", got, kgfacts.MaxFactChars)
	}
}

// TestFacts_ShortBodyDonatesItsShare pins that the reserve is a floor, not a
// quota: a one-line body leaves the aspects nearly the whole budget rather than
// permanently costing them 40%.
func TestFacts_ShortBodyDonatesItsShare(t *testing.T) {
	camp := uuid.New()
	var aspects []storage.KGNodeAspect
	for i := range 30 {
		aspects = append(aspects, storage.KGNodeAspect{
			Position: i, Key: "Fact", Value: strings.Repeat("a", 60),
		})
	}
	short := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "N",
		Body: "tiny", Aspects: aspects,
	}}}
	r := newRecaller(t, short, &fakeMetrics{})
	facts := r.Facts(liveCtx(camp), testAgent.String())

	_, content, _ := strings.Cut(facts[0], "\n")
	aspectPart, bodyPart, _ := strings.Cut(content, "\n\n")
	if bodyPart != "tiny" {
		t.Errorf("body = %q, want it whole", bodyPart)
	}
	// With a 4-rune body, the aspects should get far more than the 60% floor.
	if got := len([]rune(aspectPart)); got < int(float64(kgfacts.MaxFactChars)*0.85) {
		t.Errorf("aspects got %d runes; a short body must donate its unused share", got)
	}
}

// TestFacts_RendersRelationClause pins #546's prompt half: a relation with
// texture reaches the block as exactly ONE clause. "Knows and despises" is the
// difference between a flat NPC and a live one.
func TestFacts_RendersRelationClause(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{
			ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Mira Vance",
			RelationDisposition: -2,
		},
		{
			ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Gundren",
			RelationNote: "owes you money since the siege", RelationDisposition: 1,
		},
	}}
	r := newRecaller(t, nodes, &fakeMetrics{})

	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if !strings.Contains(facts[0], "You despise Mira Vance.") {
		t.Errorf("fact[0] = %q, want the generated disposition clause", facts[0])
	}
	// The GM's own note wins over the generated sentence — and only ONE of them
	// appears, because two clauses per neighbour is a second budget nobody agreed
	// to.
	if !strings.Contains(facts[1], "owes you money since the siege") {
		t.Errorf("fact[1] = %q, want the GM's note", facts[1])
	}
	if strings.Contains(facts[1], "You are fond of") {
		t.Errorf("fact[1] = %q, want the note INSTEAD of the generated clause", facts[1])
	}
}

// TestFacts_NeutralRelationSaysNothing pins the byte-identical guarantee: an Edge
// with no note and a neutral disposition renders exactly as before #546.
func TestFacts_NeutralRelationSaysNothing(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{
		{ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNote, Name: "The Bell", Body: "It tolls at dusk."},
	}}
	r := newRecaller(t, nodes, &fakeMetrics{})
	facts := r.Facts(liveCtx(camp), testAgent.String())
	if len(facts) != 1 || facts[0] != "### The Bell (Note)\nIt tolls at dusk." {
		t.Errorf("fact = %q, want the pre-#546 rendering unchanged", facts)
	}
}

// TestFacts_RelationClauseIsBounded pins that a long note cannot become a second
// prose block competing with the world facts.
func TestFacts_RelationClauseIsBounded(t *testing.T) {
	camp := uuid.New()
	nodes := &fakeNodes{nodes: []storage.KGNode{{
		ID: uuid.New(), CampaignID: camp, Type: storage.KGNodeNPC, Name: "Verbose",
		RelationNote: strings.Repeat("é", 900),
	}}}
	r := newRecaller(t, nodes, &fakeMetrics{})
	facts := r.Facts(liveCtx(camp), testAgent.String())
	_, content, _ := strings.Cut(facts[0], "\n")
	if got := len([]rune(content)); got > kgfacts.MaxRelationChars+4 {
		t.Errorf("relation clause = %d runes, want at most %d", got, kgfacts.MaxRelationChars)
	}
}
