package planchat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// --- fakes -------------------------------------------------------------------

// fakeStore is an in-memory planchat.Store.
type fakeStore struct {
	butler   storage.Agent
	grants   []storage.ToolGrant
	configs  map[uuid.UUID]storage.ProviderConfig
	tenant   map[storage.Component]storage.ProviderConfig
	thread   storage.PlanningThread
	messages []storage.PlanningMessage
	usage    []storage.UsageRow

	appendErr error
	nextSeq   int64
}

func newFakeStore(campaignID uuid.UUID) *fakeStore {
	threadID := uuid.New()
	return &fakeStore{
		butler: storage.Agent{
			ID:         uuid.New(),
			CampaignID: campaignID,
			Role:       storage.AgentRoleButler,
			Name:       "Glyphoxa",
			Persona:    "Helpful, dry-witted campaign butler.",
		},
		configs: map[uuid.UUID]storage.ProviderConfig{},
		tenant:  map[storage.Component]storage.ProviderConfig{},
		thread:  storage.PlanningThread{ID: threadID, CampaignID: campaignID},
	}
}

func (f *fakeStore) GetButler(_ context.Context, campaignID uuid.UUID) (storage.Agent, error) {
	if campaignID != f.butler.CampaignID {
		return storage.Agent{}, storage.ErrNotFound
	}
	return f.butler, nil
}

func (f *fakeStore) ListToolGrantsFor(_ context.Context, agentID uuid.UUID, surface storage.GrantSurface) ([]storage.ToolGrant, error) {
	if surface != storage.GrantSurfaceChat {
		return nil, errors.New("fake: unexpected surface " + string(surface))
	}
	var out []storage.ToolGrant
	for _, g := range f.grants {
		if g.AgentID == agentID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) GetProviderConfig(_ context.Context, id uuid.UUID) (storage.ProviderConfig, error) {
	cfg, ok := f.configs[id]
	if !ok {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	return cfg, nil
}

func (f *fakeStore) GetProviderConfigByComponent(_ context.Context, _ uuid.UUID, component storage.Component) (storage.ProviderConfig, error) {
	cfg, ok := f.tenant[component]
	if !ok {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	return cfg, nil
}

func (f *fakeStore) GetPlanningThread(_ context.Context, campaignID, id uuid.UUID) (storage.PlanningThread, error) {
	if id != f.thread.ID || campaignID != f.thread.CampaignID {
		return storage.PlanningThread{}, storage.ErrNotFound
	}
	return f.thread, nil
}

func (f *fakeStore) ListPlanningMessages(_ context.Context, _, _ uuid.UUID) ([]storage.PlanningMessage, error) {
	return append([]storage.PlanningMessage(nil), f.messages...), nil
}

func (f *fakeStore) AppendPlanningMessage(_ context.Context, campaignID, threadID uuid.UUID, role, content string) (storage.PlanningMessage, error) {
	if f.appendErr != nil {
		return storage.PlanningMessage{}, f.appendErr
	}
	f.nextSeq++
	m := storage.PlanningMessage{
		ID: uuid.New(), ThreadID: threadID, CampaignID: campaignID,
		Seq: f.nextSeq, Role: role, Content: content,
	}
	f.messages = append(f.messages, m)
	return m, nil
}

func (f *fakeStore) RenamePlanningThread(_ context.Context, _, _ uuid.UUID, title string) (storage.PlanningThread, error) {
	f.thread.Title = title
	return f.thread, nil
}

func (f *fakeStore) ListNodeAppearances(_ context.Context, _, _ uuid.UUID, _ int) ([]storage.AppearanceHit, error) {
	return nil, nil
}

func (f *fakeStore) AddUsage(_ context.Context, rows []storage.UsageRow) error {
	f.usage = append(f.usage, rows...)
	return nil
}

// fakeSearcher records queries and returns scripted results.
type fakeSearcher struct {
	transcriptQueries []string
	hits              []search.TranscriptHit
}

func (f *fakeSearcher) SearchNodes(context.Context, uuid.UUID, search.Access, string, int) ([]storage.KGNode, error) {
	return nil, nil
}

func (f *fakeSearcher) SearchTranscripts(_ context.Context, _ uuid.UUID, _ search.Access, query string, _ int) (search.TranscriptResults, error) {
	f.transcriptQueries = append(f.transcriptQueries, query)
	return search.TranscriptResults{Hits: f.hits}, nil
}

// fakeWriter / fakeRecapper satisfy the tool seams; the engine ctor requires
// them even when a test's grants never arm the corresponding tools.
type fakeWriter struct{ proposals int }

func (f *fakeWriter) OwnNode(context.Context, string) (tool.KGNodeRef, bool, error) {
	return tool.KGNodeRef{}, false, nil
}
func (f *fakeWriter) CreateProposal(context.Context, string, tool.ProposedWrite) error {
	f.proposals++
	return nil
}
func (f *fakeWriter) ExistingKnowledge(context.Context, string, tool.ProposedWrite) (tool.KnownForTarget, error) {
	return tool.KnownForTarget{}, nil
}

type fakeRecapper struct{}

func (fakeRecapper) RecapLastSessions(context.Context, int) (string, error) {
	return "Last session: the party met Bart.", nil
}

// scriptedProvider replays one llm.StreamEvent script per Complete call.
type scriptedProvider struct {
	calls   int
	scripts [][]llm.StreamEvent
}

func (p *scriptedProvider) Complete(_ context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	script := p.scripts[min(p.calls, len(p.scripts)-1)]
	p.calls++
	ch := make(chan llm.StreamEvent, len(script))
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// collectEvents records the exchange's stream.
type collectEvents struct {
	text  strings.Builder
	tools []string
}

func (c *collectEvents) Text(delta string) error { c.text.WriteString(delta); return nil }
func (c *collectEvents) Tool(name string, _ bool) {
	c.tools = append(c.tools, name)
}

// textDone is a one-round script: prose, then done.
func textDone(text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.EventText, Text: text},
		{Type: llm.EventUsage, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}},
		{Type: llm.EventDone, StopReason: "end_turn"},
	}
}

func newTestEngine(t *testing.T, st *fakeStore, provider llm.Provider, opts ...Option) (*Engine, *fakeSearcher, *fakeWriter) {
	t.Helper()
	searcher := &fakeSearcher{}
	writer := &fakeWriter{}
	opts = append(opts, WithProviderFactory(func(string, string) (llm.Provider, error) {
		return provider, nil
	}))
	eng := NewEngine(st, nil, searcher, writer, fakeRecapper{}, nil, nil, opts...)
	return eng, searcher, writer
}

// --- tests -------------------------------------------------------------------

func TestExchange_StreamsPersistsAndTitles(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("Here is a plan.")}}
	eng, _, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	res, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID,
		"Draft three hooks for next session", &ev)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := ev.text.String(); got != "Here is a plan." {
		t.Errorf("streamed text = %q", got)
	}
	if len(st.messages) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(st.messages))
	}
	if st.messages[0].Role != storage.PlanningRoleUser || st.messages[1].Role != storage.PlanningRoleAssistant {
		t.Errorf("roles = %s/%s", st.messages[0].Role, st.messages[1].Role)
	}
	if st.messages[1].Content != "Here is a plan." {
		t.Errorf("assistant content = %q", st.messages[1].Content)
	}
	// The first exchange auto-titles the unnamed thread from the user message.
	if res.Thread.Title != "Draft three hooks for next session" {
		t.Errorf("auto-title = %q", res.Thread.Title)
	}
	// Per-exchange ledger flush (ADR-0062): the usage row landed with tenant
	// attribution and non-zero token counts.
	if len(st.usage) == 0 {
		t.Fatalf("no usage rows flushed")
	}
	if st.usage[0].LLMInputTokens != 10 || st.usage[0].LLMOutputTokens != 5 {
		t.Errorf("usage tokens = %d/%d, want 10/5", st.usage[0].LLMInputTokens, st.usage[0].LLMOutputTokens)
	}
}

func TestExchange_ToolCallRoundFiresEventAndGrantGate(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	st.grants = []storage.ToolGrant{{AgentID: st.butler.ID, ToolName: "search_transcripts", Surface: storage.GrantSurfaceChat}}
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "c1", Name: "search_transcripts", Input: []byte(`{"query":"the cult"}`)}},
			{Type: llm.EventDone, StopReason: "tool_use"},
		},
		textDone("The cult last appeared at the docks."),
	}}
	eng, searcher, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID,
		"What happened with the cult?", &ev)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if len(searcher.transcriptQueries) != 1 || searcher.transcriptQueries[0] != "the cult" {
		t.Errorf("searcher queries = %v", searcher.transcriptQueries)
	}
	if len(ev.tools) != 1 || ev.tools[0] != "search_transcripts" {
		t.Errorf("tool events = %v", ev.tools)
	}
}

func TestExchange_UngrantedToolIsRefused(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	// NO chat grants: the model hallucinates a tool call anyway; the loop must
	// feed back an error result, never execute (ADR-0029), and the second round
	// answers in prose.
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "c1", Name: "search_transcripts", Input: []byte(`{"query":"x"}`)}},
			{Type: llm.EventDone, StopReason: "tool_use"},
		},
		textDone("Answering without tools."),
	}}
	eng, searcher, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "hi", &ev)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if len(searcher.transcriptQueries) != 0 {
		t.Errorf("ungranted tool executed: %v", searcher.transcriptQueries)
	}
}

// exhaustedAllowance reports a spent monthly allowance.
type exhaustedAllowance struct{}

func (exhaustedAllowance) AllowanceState(context.Context, uuid.UUID) (spend.AllowanceState, error) {
	included := 10.0
	return spend.AllowanceState{IncludedUSD: &included, MonthUSD: 10.0}, nil
}

func TestExchange_AllowanceExhaustedRefusesBeforeAnything(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("never reached")}}
	eng, _, _ := newTestEngine(t, st, provider, WithAllowance(exhaustedAllowance{}))

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "hi", &ev)
	if !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("err = %v, want ErrAllowanceExhausted", err)
	}
	if provider.calls != 0 {
		t.Errorf("provider called %d times despite refusal", provider.calls)
	}
	if len(st.messages) != 0 {
		t.Errorf("refused exchange persisted %d messages", len(st.messages))
	}
}

// erroringAllowance fails the allowance read.
type erroringAllowance struct{}

func (erroringAllowance) AllowanceState(context.Context, uuid.UUID) (spend.AllowanceState, error) {
	return spend.AllowanceState{}, errors.New("ledger down")
}

func TestExchange_AllowanceReadFailureFailsClosed(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("never reached")}}
	eng, _, _ := newTestEngine(t, st, provider, WithAllowance(erroringAllowance{}))

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "hi", &ev)
	if err == nil || provider.calls != 0 {
		t.Fatalf("err = %v, provider calls = %d; want fail-closed refusal", err, provider.calls)
	}
}

func TestExchange_EmptyModelAnswerIsErrorNotPersisted(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("   ")}}
	eng, _, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "hi", &ev)
	if err == nil {
		t.Fatalf("want error on empty answer")
	}
	// The user message stays (crash-safe posture); no assistant row.
	for _, m := range st.messages {
		if m.Role == storage.PlanningRoleAssistant {
			t.Errorf("empty answer was persisted: %+v", m)
		}
	}
}

func TestExchange_UnknownThreadIsNotFound(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("x")}}
	eng, _, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, uuid.New(), "hi", &ev)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveChatLLMConfig_Ladder(t *testing.T) {
	campaignID := uuid.New()
	tenantID := uuid.New()
	st := newFakeStore(campaignID)
	eng, _, _ := newTestEngine(t, st, &scriptedProvider{scripts: [][]llm.StreamEvent{textDone("x")}})

	// Rung 4: nothing configured → nil config, chat_llm label.
	cfg, component, err := eng.resolveChatLLMConfig(context.Background(), tenantID, st.butler)
	if err != nil || cfg != nil || component != storage.ComponentChatLLM {
		t.Fatalf("empty ladder = (%v, %s, %v)", cfg, component, err)
	}

	// Rung 3: tenant 'llm' config.
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "groq", Model: "voice-model"}
	cfg, component, err = eng.resolveChatLLMConfig(context.Background(), tenantID, st.butler)
	if err != nil || cfg == nil || cfg.Model != "voice-model" || component != storage.ComponentLLM {
		t.Fatalf("llm rung = (%+v, %s, %v)", cfg, component, err)
	}

	// Rung 2: tenant 'chat_llm' outranks it.
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "groq", Model: "chat-model"}
	cfg, _, err = eng.resolveChatLLMConfig(context.Background(), tenantID, st.butler)
	if err != nil || cfg == nil || cfg.Model != "chat-model" {
		t.Fatalf("chat_llm rung = (%+v, %v)", cfg, err)
	}

	// Rung 1: the Butler's own chat slot outranks everything.
	id := uuid.New()
	st.configs[id] = storage.ProviderConfig{ID: id, Provider: "anthropic", Model: "butler-model"}
	st.butler.ChatLLMProviderConfigID = uuid.NullUUID{UUID: id, Valid: true}
	cfg, _, err = eng.resolveChatLLMConfig(context.Background(), tenantID, st.butler)
	if err != nil || cfg == nil || cfg.Model != "butler-model" {
		t.Fatalf("butler rung = (%+v, %v)", cfg, err)
	}
}

func TestTrimHistory_KeepsNewestWithinBudget(t *testing.T) {
	long := strings.Repeat("x", historyCharBudget) // one message eating the whole budget
	history := []storage.PlanningMessage{
		{Role: storage.PlanningRoleUser, Content: long},
		{Role: storage.PlanningRoleAssistant, Content: "recent answer"},
		{Role: storage.PlanningRoleUser, Content: "recent question"},
	}
	kept := trimHistory(history)
	if len(kept) != 2 {
		t.Fatalf("kept = %d messages, want the 2 newest", len(kept))
	}
	if kept[0].Content != "recent answer" || kept[1].Content != "recent question" {
		t.Errorf("kept wrong messages: %+v", kept)
	}
}

func TestAutoTitle(t *testing.T) {
	if got := autoTitle("Plan the heist\nwith details"); got != "Plan the heist" {
		t.Errorf("autoTitle multiline = %q", got)
	}
	long := strings.Repeat("ä", autoTitleRunes+10)
	if got := autoTitle(long); got != strings.Repeat("ä", autoTitleRunes)+"…" {
		t.Errorf("autoTitle long = %q (len %d)", got, len([]rune(got)))
	}
}

func TestStablePrefix_IsStableAcrossExchanges(t *testing.T) {
	butler := storage.Agent{Name: "Glyphoxa", Persona: "Dry wit."}
	a := stablePrefix(butler)
	b := stablePrefix(butler)
	if a != b {
		t.Errorf("stable prefix differs between calls")
	}
	if !strings.Contains(a, "Dry wit.") {
		t.Errorf("persona missing from prefix: %q", a)
	}
}
