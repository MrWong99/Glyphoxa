package llmbench

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// fakeLLM is a keyless [llm.Provider] that answers like a well-behaved model:
// a dice request gets a native dice call and then a narrated result, a
// remember request a remember_knowledge call, everything else one sentence. It
// records every request so a test can read the production prompt back.
type fakeLLM struct {
	mu       sync.Mutex
	requests []llm.Request
	// reply overrides the plain-text answer when set.
	reply string
}

func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	last := req.Messages[len(req.Messages)-1]
	var user string
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			user = m.Text
		}
	}
	hasTool := func(name string) bool {
		for _, t := range req.Tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}

	var events []llm.StreamEvent
	switch {
	case last.Role == llm.RoleTool:
		events = text("Eine " + last.ToolResults[0].Content + " Na bitte.")
	case strings.Contains(user, "Wirf") && hasTool("dice") && req.ToolChoice.Mode != llm.ToolChoiceNone:
		events = []llm.StreamEvent{
			{Type: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "call_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}},
			{Type: llm.EventUsage, Usage: llm.Usage{InputTokens: 1000, OutputTokens: 20}},
			{Type: llm.EventDone, StopReason: "tool_use"},
		}
	case strings.Contains(user, "merk dir") && hasTool("remember_knowledge"):
		events = []llm.StreamEvent{
			{Type: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "call_2", Name: "remember_knowledge", Input: json.RawMessage(`{"kind":"fact","fact":"Mira schuldet zwei Silber."}`)}},
			{Type: llm.EventUsage, Usage: llm.Usage{InputTokens: 1000, OutputTokens: 20}},
			{Type: llm.EventDone, StopReason: "tool_use"},
		}
	default:
		reply := f.reply
		if reply == "" {
			reply = "Na klar, meine Liebe. Setz dich."
		}
		events = text(reply)
	}

	ch := make(chan llm.StreamEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func text(s string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.EventText, Text: s},
		{Type: llm.EventUsage, Usage: llm.Usage{InputTokens: 2000, OutputTokens: 30}},
		{Type: llm.EventDone, StopReason: "end_turn"},
	}
}

func (f *fakeLLM) request(t *testing.T, n int) llm.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.requests) {
		t.Fatalf("only %d requests, want #%d", len(f.requests), n+1)
	}
	return f.requests[n]
}

func fakeCandidate(f *fakeLLM) Candidate {
	return Candidate{
		Name:     "fake",
		Provider: observe.ProviderGroq,
		Model:    "openai/gpt-oss-120b",
		New:      func() llm.Provider { return f },
	}
}

// runOne runs a single-script corpus against the fake and returns its turns.
func runOne(t *testing.T, f *fakeLLM, turns ...Turn) []TurnResult {
	t.Helper()
	reports := Runner{
		Candidates: []Candidate{fakeCandidate(f)},
		Scripts:    []Script{{Name: "s", Turns: turns}},
		Sleep:      func(context.Context, time.Duration) {},
	}.Run(t.Context())
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	return reports[0].Turns
}

// TestRunner_DrivesTheProductionPrompt pins that a bench turn goes through the
// real prompt assembly of pkg/voice/agent: the persona, the speaker roster, the
// spoken-output rule and the TTS markup instruction are in the system prompt,
// and the user line carries the speaker's name.
func TestRunner_DrivesTheProductionPrompt(t *testing.T) {
	f := &fakeLLM{}
	runOne(t, f, mira("Hallo Flo!", TagSmallTalk))

	req := f.request(t, 0)
	if req.Model != "openai/gpt-oss-120b" {
		t.Errorf("request model = %q, want the candidate's", req.Model)
	}
	sys := req.Messages[0].Text
	for _, want := range []string{"Du bist Flo", "Mira, Brak", "spoken aloud by a voice", "eleven_v3"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, sys)
		}
	}
	if got := req.Messages[len(req.Messages)-1].Text; got != "Mira: Hallo Flo!" {
		t.Errorf("user line = %q, want the speaker-attributed line", got)
	}
}

// TestRunner_CarriesHistoryAcrossTheTurnsOfAScript pins the multi-turn
// property: what was spoken in turn 1 is in the prompt of turn 2.
func TestRunner_CarriesHistoryAcrossTheTurnsOfAScript(t *testing.T) {
	f := &fakeLLM{}
	turns := runOne(t, f, mira("Hallo Flo!", TagSmallTalk), brak("Und ich?", TagSmallTalk))
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}

	var seen []string
	for _, m := range f.request(t, 1).Messages {
		seen = append(seen, string(m.Role)+": "+m.Text)
	}
	joined := strings.Join(seen, "\n")
	for _, want := range []string{"user: Mira: Hallo Flo!", "assistant: Na klar, meine Liebe. Setz dich.", "user: Brak: Und ich?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("turn 2 prompt lacks %q:\n%s", want, joined)
		}
	}
}

// TestRunner_RecordsADiceTurn pins the per-turn record of a tool turn through
// the real engine: the dice gate arms on the German request, the real dice
// Tool runs, and the turn reports its rounds, calls, tokens and cost.
func TestRunner_RecordsADiceTurn(t *testing.T) {
	f := &fakeLLM{}
	turn := runOne(t, f, brak("Wirf einen W20 für mich.", TagDiceSingle).expect(toolDice))[0]

	if turn.Error != "" {
		t.Fatalf("turn error: %s", turn.Error)
	}
	if turn.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2 (tool round + answer)", turn.Rounds)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != "dice" || !turn.ToolCalls[0].SchemaValid {
		t.Errorf("ToolCalls = %+v, want one schema-valid dice call", turn.ToolCalls)
	}
	if !turn.ToolsOK {
		t.Error("ToolsOK = false, want the expected dice call to satisfy the turn")
	}
	if turn.InputTokens != 3000 || turn.OutputTokens != 50 {
		t.Errorf("tokens = %d in / %d out, want 3000 / 50 (both rounds)", turn.InputTokens, turn.OutputTokens)
	}
	// gpt-oss-120b: $0.15 in / $0.60 out per MTok.
	if want := 3000*0.15/1e6 + 50*0.60/1e6; turn.CostUSD < want*0.999 || turn.CostUSD > want*1.001 {
		t.Errorf("CostUSD = %g, want %g", turn.CostUSD, want)
	}
	if !strings.HasSuffix(turn.Reply, "Na bitte.") {
		t.Errorf("Reply = %q, want the narrated result", turn.Reply)
	}
	if turn.FirstDeltaMS < 0 || turn.FirstSentenceMS < 0 || turn.EndMS < turn.FirstSentenceMS {
		t.Errorf("timings = delta %v, sentence %v, end %v; want all set and ordered", turn.FirstDeltaMS, turn.FirstSentenceMS, turn.EndMS)
	}
}

// TestRunner_FakeDepsMakeTheKnowledgeToolsCallable pins that the knowledge
// Tools are backed: a remember_knowledge call lands as a proposal instead of
// failing "unavailable in this mode".
func TestRunner_FakeDepsMakeTheKnowledgeToolsCallable(t *testing.T) {
	f := &fakeLLM{}
	turn := runOne(t, f, mira("Flo, merk dir bitte: Ich schulde dir zwei Silber.", TagRememberAccept).expect(toolRemember))[0]

	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != toolRemember {
		t.Fatalf("ToolCalls = %+v, want the remember_knowledge call", turn.ToolCalls)
	}
	if turn.ToolErrors != 0 {
		t.Errorf("ToolErrors = %d, want 0: the fake writer accepts the proposal", turn.ToolErrors)
	}
	if !turn.ToolsOK {
		t.Error("ToolsOK = false")
	}
}

// TestRunner_FlagsWhatTheDetectorsFind pins that the detectors run over both
// the raw model text and what would be spoken, and that an unexpected tool
// call fails the turn's tool check.
func TestRunner_FlagsWhatTheDetectorsFind(t *testing.T) {
	f := &fakeLLM{reply: "*wischt den Tresen* Na, mein Lieber, was darf's sein?"}
	turn := runOne(t, f, mira("Hallo Flo!", TagSmallTalk))[0]

	if turn.RawFindings.StageDirections == 0 {
		t.Errorf("RawFindings = %+v, want the asterisk action found in the raw text", turn.RawFindings)
	}
	if turn.SpokenFindings.GenderSlip != 1 {
		t.Errorf("SpokenFindings = %+v, want the masculine address to Mira found", turn.SpokenFindings)
	}
	if strings.Contains(turn.Reply, "*") {
		t.Errorf("Reply = %q: the spoken text went through the production scrub and has no asterisks", turn.Reply)
	}
}

// TestRunner_InterleavesCandidates pins the fairness property: the candidates
// take turns within a script instead of running one after another, so no one
// candidate owns a time window.
func TestRunner_InterleavesCandidates(t *testing.T) {
	var mu sync.Mutex
	var order []string
	mk := func(name string) Candidate {
		f := &fakeLLM{}
		c := fakeCandidate(f)
		c.Name = name
		c.New = func() llm.Provider {
			return orderLLM{f, func() { mu.Lock(); order = append(order, name); mu.Unlock() }}
		}
		return c
	}
	slept := 0
	reports := Runner{
		Candidates: []Candidate{mk("a"), mk("b")},
		Scripts:    []Script{{Name: "s", Turns: []Turn{mira("Eins.", TagSmallTalk), mira("Zwei.", TagSmallTalk)}}},
		Delay:      time.Second,
		Sleep:      func(context.Context, time.Duration) { slept++ },
	}.Run(t.Context())

	if got := strings.Join(order, ""); got != "abba" {
		t.Errorf("call order = %q, want the candidates interleaved and rotated per turn (abba)", got)
	}
	if slept != 4 {
		t.Errorf("paced %d times, want once per call (4)", slept)
	}
	if len(reports) != 2 || reports[0].Candidate.Name != "a" || len(reports[0].Turns) != 2 || len(reports[1].Turns) != 2 {
		t.Errorf("reports = %+v, want both candidates with both turns, in table order", reports)
	}
}

type orderLLM struct {
	llm.Provider
	onCall func()
}

func (o orderLLM) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	o.onCall()
	return o.Provider.Complete(ctx, req)
}
