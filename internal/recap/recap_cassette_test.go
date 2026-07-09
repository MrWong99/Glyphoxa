package recap

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
)

// countingProvider wraps a cassette provider to count Complete calls (the map/reduce
// fan-out observation) while replaying the recorded exchanges unchanged.
type countingProvider struct {
	inner llm.Provider
	calls *int
}

func (p countingProvider) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	*p.calls++
	return p.inner.Complete(ctx, req)
}

const cassetteButlerPersona = "You are Gimble, the wise and slightly weary Butler who chronicles the party's deeds with dry wit."

// singleCassetteLines is the fixed fixture transcript for the single-call cassette.
// The distinctive proper nouns (Aethelred, Sunstone, crypt) let a tolerant,
// normalized assertion confirm the recap is about THIS session without pinning exact
// wording (the model paraphrases).
func singleCassetteLines() []storage.TranscriptLine {
	return []storage.TranscriptLine{
		{Seq: 1, Who: "GM", Text: "The party arrives at the ruined keep of Aethelred as dusk falls."},
		{Seq: 2, Who: "Bart", Tag: "npc", Text: "Halt! None pass the gate of Aethelred without paying the toll."},
		{Seq: 3, Who: "Alice", Tag: "player", Text: "We carry the Sunstone relic and seek the buried crypt beneath the keep."},
		{Seq: 4, Who: "GM", Text: "A cold wind stirs; the crypt door grinds slowly open before them."},
		{Seq: 5, Who: "Bart", Tag: "npc", Text: "Then your doom is your own, relic-bearers. The dead do not forgive trespass."},
	}
}

// TestRecapSingleCassette is the AC1 determinism proof: a seeded session recapped
// through a cassette-recorded Groq completion (ADR-0021) yields coherent text about
// this session, Windowed=false, with a system prompt carrying the Butler Persona and
// the Campaign Language.
func TestRecapSingleCassette(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: cassetteButlerPersona}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), singleCassetteLines())

	var sysSeen string
	calls := 0
	factory := func(_, _ string) (llm.Provider, error) {
		cass := voicecassette.LoadLLM(t, "llm-recap-single")
		return countingProvider{inner: capturingProvider{inner: cass, sys: &sysSeen}, calls: &calls}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))
	res, err := eng.Recap(context.Background(), []uuid.UUID{sid})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if res.Windowed {
		t.Error("Windowed = true, want false for a short session")
	}
	if calls != 1 {
		t.Errorf("Complete calls = %d, want 1 (single-call path)", calls)
	}
	norm := strings.ToLower(res.Text)
	if len(strings.TrimSpace(norm)) < 40 {
		t.Errorf("recap too short to be coherent: %q", res.Text)
	}
	salient := []string{"aethelred", "sunstone", "relic", "crypt"}
	hits := 0
	for _, s := range salient {
		if strings.Contains(norm, s) {
			hits++
		}
	}
	if hits < 2 {
		t.Errorf("recap does not reference the session (hits=%d): %q", hits, res.Text)
	}
	if !strings.Contains(sysSeen, "Gimble") {
		t.Errorf("system prompt missing Butler Persona: %q", sysSeen)
	}
	if !strings.Contains(sysSeen, "English") {
		t.Errorf("system prompt missing Campaign Language: %q", sysSeen)
	}
}

// windowedCassetteLines deterministically generates an over-budget transcript
// (> singleCallBudgetChars) so the recap map-reduces. Each line's text is distinct so
// the two window prompts hash differently.
func windowedCassetteLines() []storage.TranscriptLine {
	const n = 40
	lines := make([]storage.TranscriptLine, n)
	for i := 0; i < n; i++ {
		who := "GM"
		if i%2 == 1 {
			who = "Alice"
		}
		sentence := fmt.Sprintf("In chapter %d the heroes explored region %d, bargained with faction %d, and fought the beast of hollow %d. ", i, i, i, i)
		lines[i] = storage.TranscriptLine{Seq: int64(i + 1), Who: who, Text: strings.Repeat(sentence, 6)}
	}
	return lines
}

// TestRecapWindowedCassette is the oversized-fixture proof: an over-budget session
// map-reduces through the cassette — two map calls plus one reduce — and reports
// Windowed=true (ADR-0021 determinism over the long-session strategy).
func TestRecapWindowedCassette(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: cassetteButlerPersona}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), windowedCassetteLines())

	calls := 0
	factory := func(_, _ string) (llm.Provider, error) {
		return countingProvider{inner: voicecassette.LoadLLM(t, "llm-recap-windowed"), calls: &calls}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))
	res, err := eng.Recap(context.Background(), []uuid.UUID{sid})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if !res.Windowed {
		t.Error("Windowed = false, want true for an over-budget session")
	}
	if calls != 3 {
		t.Errorf("Complete calls = %d, want 3 (2 map + 1 reduce)", calls)
	}
	if len(strings.TrimSpace(res.Text)) < 40 {
		t.Errorf("reduced recap too short: %q", res.Text)
	}
}

// capturingProvider records the system prompt of the first request it sees, so the
// cassette test can assert Persona/Language without pinning exact wording.
type capturingProvider struct {
	inner llm.Provider
	sys   *string
}

func (p capturingProvider) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	if *p.sys == "" && len(req.Messages) > 0 {
		*p.sys = req.Messages[0].Text
	}
	return p.inner.Complete(ctx, req)
}
