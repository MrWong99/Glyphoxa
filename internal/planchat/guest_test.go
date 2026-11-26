package planchat

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// A Campaign Guest holding planning.use but not transcripts.read
// (ADR-0065) chats without the tools that read the spoken record.

func toolNames(t *testing.T, e *Engine, ctx context.Context) []string {
	t.Helper()
	reg, err := e.chatTools(ctx, uuid.New())
	if err != nil {
		t.Fatalf("chatTools: %v", err)
	}
	var names []string
	for _, tl := range reg.Tools() {
		names = append(names, tl.Name())
	}
	sort.Strings(names)
	return names
}

func TestChatTools_WithoutTranscriptsDropsEveryTranscriptReader(t *testing.T) {
	eng, _, _ := newTestEngine(t, newFakeStore(uuid.New()), &scriptedProvider{})

	full := toolNames(t, eng, context.Background())
	want := []string{"appearances_of", "find_node", "propose_knowledge", "recap", "search_transcripts"}
	if !slices.Equal(full, want) {
		t.Fatalf("the GM's belt = %v, want %v", full, want)
	}
	if TranscriptsWithheld(context.Background()) {
		t.Fatal("a plain context reads as withheld")
	}

	ctx := WithoutTranscripts(context.Background())
	if !TranscriptsWithheld(ctx) {
		t.Fatal("WithoutTranscripts did not mark the context")
	}
	// What is left reads the wiki and proposes; nothing reads a transcript.
	if got := toolNames(t, eng, ctx); !slices.Equal(got, []string{"find_node", "propose_knowledge"}) {
		t.Fatalf("belt without transcripts = %v, want only find_node and propose_knowledge", got)
	}
}

// Even when the Butler's grant rows allow search_transcripts and the model
// asks for it, an exchange marked WithoutTranscripts never runs it: the tool
// is not registered, and a grant for an unregistered tool grants nothing.
func TestExchange_WithoutTranscriptsRefusesAGrantedTranscriptTool(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	st.grants = []storage.ToolGrant{
		{AgentID: st.butler.ID, ToolName: "search_transcripts", Surface: storage.GrantSurfaceChat},
		{AgentID: st.butler.ID, ToolName: "recap", Surface: storage.GrantSurfaceChat},
		{AgentID: st.butler.ID, ToolName: "appearances_of", Surface: storage.GrantSurfaceChat},
	}
	provider := &scriptedProvider{scripts: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "c1", Name: "search_transcripts", Input: []byte(`{"query":"the cult"}`)}},
			{Type: llm.EventDone, StopReason: "tool_use"},
		},
		textDone("I cannot look that up."),
	}}
	eng, searcher, _ := newTestEngine(t, st, provider)

	var ev collectEvents
	_, err := eng.Exchange(WithoutTranscripts(context.Background()), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID,
		"What did we say about the cult?", &ev)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if len(searcher.transcriptQueries) != 0 {
		t.Fatalf("the transcript search ran %v for an exchange without transcripts", searcher.transcriptQueries)
	}
}
