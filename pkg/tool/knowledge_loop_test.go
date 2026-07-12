package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestLoopRunsKnowledgeToolsInline is the ADR-0021/ADR-0030 pin for the two new
// built-ins: granted kg_query and transcript_search execute INLINE inside the
// tool-use loop (they are read-only, so the loop never refuses them), and their
// rendered results are fed back to the model as tool-role messages. It drives the
// loop with a scripted provider over fake sources — the framework's cassette.
func TestLoopRunsKnowledgeToolsInline(t *testing.T) {
	kg := &fakeKG{searchFacts: []KGFact{{Name: "The Duke", Type: "NPC", Body: "rules the city"}}}
	ts := &fakeTranscript{hits: []TranscriptHit{{Who: "GM", Kind: "human", Text: "you promised the Duke a favour"}}}
	reg := BuiltinRegistry(Deps{KG: kg, Transcripts: ts})
	gs := NewGrantSet(reg,
		Grant{ToolName: "kg_query"},          // nil config → campaign scope
		Grant{ToolName: "transcript_search"}, // no scope
	)

	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			// Round 1: the model recalls from the KG.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "kg_query", Input: json.RawMessage(`{"query":"duke"}`)},
			}}},
			// Round 2: then searches the transcript.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c2", Name: "transcript_search", Input: json.RawMessage(`{"query":"promise"}`)},
			}}},
			// Round 3: answers with both recalled.
			{reply: AssistantMessage{Text: "Yes — you promised the Duke a favour."}},
		},
	}
	loop := NewLoop(p, gs)

	final, err := loop.Run(context.Background(), []Message{
		{Role: RoleSystem, Text: "You are Bart."},
		{Role: RoleUser, Text: "Do you remember what I promised?"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "Yes — you promised the Duke a favour." {
		t.Errorf("final = %q", final)
	}
	if !kg.searchCalled {
		t.Error("kg_query should have hit the campaign SearchFacts source")
	}
	if ts.callCount != 1 {
		t.Errorf("transcript_search called the source %d times, want 1", ts.callCount)
	}

	// The kg_query result must have been fed back as a non-error tool-role message.
	kgResult := findToolResult(t, p.seenMessages, "c1")
	if kgResult.IsError {
		t.Errorf("kg_query fed back as error (was it refused as side-effecting?): %q", kgResult.Content)
	}
	if !strings.Contains(kgResult.Content, "### The Duke (NPC)") {
		t.Errorf("kg_query result not rendered for the prompt: %q", kgResult.Content)
	}
	tsResult := findToolResult(t, p.seenMessages, "c2")
	if tsResult.IsError || !strings.Contains(tsResult.Content, "promised the Duke") {
		t.Errorf("transcript_search result = %+v", tsResult)
	}
}

// TestLoopRunsRecapInline is the ADR-0021/ADR-0030 pin for the recap built-in
// (#372): a granted recap executes INLINE inside the tool-use loop (read-only, so
// never refused) and its returned prose is fed back to the model as a tool-role
// message it relays. It drives the loop with a scripted provider over a fake
// Recapper — the framework's cassette.
func TestLoopRunsRecapInline(t *testing.T) {
	const prose = "Last session, the party stormed the keep and freed the Duke."
	rc := &fakeRecapper{text: prose}
	reg := BuiltinRegistry(Deps{Recap: rc})
	gs := NewGrantSet(reg, Grant{ToolName: "recap"}) // no scope

	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			// Round 1: the model asks for a recap of the last session.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "recap", Input: json.RawMessage(`{"sessions":1}`)},
			}}},
			// Round 2: relays it to the players.
			{reply: AssistantMessage{Text: prose}},
		},
	}
	loop := NewLoop(p, gs)

	final, err := loop.Run(context.Background(), []Message{
		{Role: RoleSystem, Text: "You are Glyphoxa."},
		{Role: RoleUser, Text: "Glyphoxa, recap last session."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != prose {
		t.Errorf("final = %q, want %q", final, prose)
	}
	if rc.callCount != 1 {
		t.Errorf("recap hit the source %d times, want 1", rc.callCount)
	}
	if rc.gotSessions != 1 {
		t.Errorf("recap asked for %d sessions, want 1", rc.gotSessions)
	}
	res := findToolResult(t, p.seenMessages, "c1")
	if res.IsError {
		t.Errorf("recap fed back as error (was it refused as side-effecting?): %q", res.Content)
	}
	if !strings.Contains(res.Content, "freed the Duke") {
		t.Errorf("recap result not rendered for the prompt: %q", res.Content)
	}
}

// findToolResult scans the messages the provider saw for the tool-role result
// keyed to callID.
func findToolResult(t *testing.T, seen [][]Message, callID string) ToolResult {
	t.Helper()
	for _, msgs := range seen {
		for _, m := range msgs {
			if m.Role != RoleTool {
				continue
			}
			for _, tr := range m.ToolResults {
				if tr.CallID == callID {
					return tr
				}
			}
		}
	}
	t.Fatalf("no tool result for call %q was ever fed back", callID)
	return ToolResult{}
}
