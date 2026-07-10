package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeKG is a scripted KGReader recording which scope method ran and with what
// caller/query, so the handler's scope enforcement is pinned without a DB.
type fakeKG struct {
	ownFacts    []KGFact
	searchFacts []KGFact
	err         error

	ownCalled    bool
	ownAgentID   string
	searchCalled bool
	searchQuery  string
	searchLimit  int
}

func (f *fakeKG) OwnNodeFacts(_ context.Context, agentID string) ([]KGFact, error) {
	f.ownCalled = true
	f.ownAgentID = agentID
	return f.ownFacts, f.err
}

func (f *fakeKG) SearchFacts(_ context.Context, query string, limit int) ([]KGFact, error) {
	f.searchCalled = true
	f.searchQuery = query
	f.searchLimit = limit
	return f.searchFacts, f.err
}

func TestKGQueryOwnNodeUsesCallerNeighbourhood(t *testing.T) {
	src := &fakeKG{ownFacts: []KGFact{
		{Name: "Mara", Type: "Character", Body: "You promised Mara a favour."},
		{Name: "Docks", Type: "Location", Body: "Where you met."},
	}}
	tool := NewKGQuery(src)
	ctx := WithCaller(context.Background(), "agent-42")
	cfg := json.RawMessage(`{"scope":"own_node"}`)

	out, err := tool.Execute(ctx, json.RawMessage(`{"query":"Mara"}`), cfg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !src.ownCalled {
		t.Fatal("own_node scope must read OwnNodeFacts")
	}
	if src.searchCalled {
		t.Fatal("own_node scope must NOT reach the campaign SearchFacts")
	}
	if src.ownAgentID != "agent-42" {
		t.Errorf("OwnNodeFacts agentID = %q, want the caller identity agent-42", src.ownAgentID)
	}
	// Query-term filter keeps only the Mara fact.
	if !strings.Contains(out, "Mara") || strings.Contains(out, "Docks") {
		t.Errorf("own_node result should be query-filtered to Mara: %q", out)
	}
}

// TestKGQueryOwnNodeCannotBeWidenedByArgs pins the ADR-0029 security property:
// the LLM cannot escape its own_node scope by crafting arguments — the scope and
// the caller both come from the grant/ctx, never the args, so a hostile "about"
// or a foreign agent id in the args is ignored.
func TestKGQueryOwnNodeCannotBeWidenedByArgs(t *testing.T) {
	src := &fakeKG{ownFacts: []KGFact{{Name: "Mine", Type: "Note", Body: "only mine"}}}
	ctx := WithCaller(context.Background(), "agent-self")
	cfg := json.RawMessage(`{"scope":"own_node"}`)

	// Args try to smuggle another agent id and a campaign scope — both must be inert.
	args := json.RawMessage(`{"query":"mine","agent_id":"agent-victim","scope":"campaign"}`)
	if _, err := NewKGQuery(src).Execute(ctx, args, cfg); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if src.searchCalled {
		t.Fatal("crafted args must not widen own_node to campaign SearchFacts")
	}
	if src.ownAgentID != "agent-self" {
		t.Errorf("caller stayed %q — a smuggled agent_id must be ignored", src.ownAgentID)
	}
}

// TestKGQueryNilConfigDefaultsCampaign pins S3: a grant with no scope config
// reads campaign-wide (reads are gm_private-filtered, so the wider default is
// safe) via SearchFacts, not OwnNodeFacts.
func TestKGQueryNilConfigDefaultsCampaign(t *testing.T) {
	src := &fakeKG{searchFacts: []KGFact{{Name: "The Duke", Type: "NPC", Body: "Rules the city."}}}
	tool := NewKGQuery(src)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"duke","limit":3}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !src.searchCalled {
		t.Fatal("nil config must default to the campaign SearchFacts read")
	}
	if src.ownCalled {
		t.Fatal("nil config must NOT read OwnNodeFacts")
	}
	if src.searchQuery != "duke" || src.searchLimit != 3 {
		t.Errorf("SearchFacts got (%q,%d), want (duke,3)", src.searchQuery, src.searchLimit)
	}
	if !strings.Contains(out, "### The Duke (NPC)") {
		t.Errorf("render should carry the fact header: %q", out)
	}
}

func TestKGQueryExplicitCampaignScope(t *testing.T) {
	src := &fakeKG{searchFacts: []KGFact{{Name: "X", Type: "Note", Body: "y"}}}
	cfg := json.RawMessage(`{"scope":"campaign"}`)
	if _, err := NewKGQuery(src).Execute(context.Background(), json.RawMessage(`{"query":"x"}`), cfg); err != nil {
		t.Fatal(err)
	}
	if !src.searchCalled || src.ownCalled {
		t.Error("explicit campaign scope must read SearchFacts")
	}
}

func TestKGQueryUnknownScopeErrors(t *testing.T) {
	cfg := json.RawMessage(`{"scope":"everything"}`)
	_, err := NewKGQuery(&fakeKG{}).Execute(context.Background(), json.RawMessage(`{"query":"x"}`), cfg)
	if err == nil {
		t.Fatal("an unknown scope must fail loudly, not widen silently")
	}
}

func TestKGQueryBoundsAndTruncates(t *testing.T) {
	long := strings.Repeat("z", MaxKGFactBodyRunes*2)
	facts := make([]KGFact, 10)
	for i := range facts {
		facts[i] = KGFact{Name: "N", Type: "Note", Body: long}
	}
	out, err := NewKGQuery(&fakeKG{searchFacts: facts}).Execute(
		context.Background(), json.RawMessage(`{"query":"z"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxToolResultChars {
		t.Errorf("result %d chars exceeds budget %d", len(out), MaxToolResultChars)
	}
	if !strings.Contains(out, "…") {
		t.Error("an overlong body should be truncated with an ellipsis")
	}
}

func TestKGQueryEmpty(t *testing.T) {
	out, err := NewKGQuery(&fakeKG{}).Execute(
		context.Background(), json.RawMessage(`{"query":"nothing"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matching knowledge" {
		t.Errorf("empty = %q", out)
	}
}

// TestKGQueryMultibyteFactRenders pins the reviewer's finding: a multibyte-heavy
// (CJK) fact must render (truncated) rather than collapse to "no matching
// knowledge". A byte-counted budget on the first block blew past 2000 before
// anything was written; the budget is counted in runes and the first block is
// always emitted.
func TestKGQueryMultibyteFactRenders(t *testing.T) {
	body := strings.Repeat("我", 800) // 800 CJK runes = 2400 bytes > 2000-byte budget
	src := &fakeKG{searchFacts: []KGFact{{Name: "龍", Type: "NPC", Body: body}}}
	out, err := NewKGQuery(src).Execute(context.Background(), json.RawMessage(`{"query":"dragon"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == "no matching knowledge" {
		t.Fatal("a CJK-heavy match rendered as 'none' — budget must count runes, not bytes")
	}
	if !strings.Contains(out, "### 龍 (NPC)") {
		t.Errorf("fact header missing: %q", out[:min(60, len(out))])
	}
	if utf8.RuneCountInString(out) > MaxToolResultChars {
		t.Errorf("result %d runes exceeds budget %d", utf8.RuneCountInString(out), MaxToolResultChars)
	}
}

// TestFilterByQueryUnicodeTerms pins finding #3: query tokenizing is Unicode-aware
// so German umlaut and CJK queries filter instead of dumping the whole
// neighbourhood.
func TestKGQueryOwnNodeUnicodeQueryFilters(t *testing.T) {
	src := &fakeKG{ownFacts: []KGFact{
		{Name: "Würfel des Schicksals", Type: "Item", Body: "ein magischer Würfel"},
		{Name: "Schwert", Type: "Item", Body: "eine Klinge"},
	}}
	ctx := WithCaller(context.Background(), "a1")
	out, err := NewKGQuery(src).Execute(ctx, json.RawMessage(`{"query":"Würfel"}`), json.RawMessage(`{"scope":"own_node"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Würfel des Schicksals") {
		t.Errorf("umlaut query should match the Würfel fact: %q", out)
	}
	if strings.Contains(out, "Schwert") {
		t.Errorf("umlaut query should NOT dump unrelated facts (ASCII tokenizer bug): %q", out)
	}
}
