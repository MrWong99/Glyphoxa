package agenttool_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// stubSynth is a [tts.Synthesizer] supplying a fixed audio-markup instruction;
// the Replier needs one to assemble the system prompt. Synthesize is unused.
type stubSynth struct{}

func (stubSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (stubSynth) AudioMarkupPrompt(tts.Voice) string { return "Speak plainly." }

// addressed builds an AddressRouted event targeting the given Agent.
func addressed(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: agentID},
	}
}

// Compile-time assertion: the bridge Engine satisfies the agent seam.
var _ agent.Engine = (*agenttool.Engine)(nil)

// scriptedProvider is a deterministic streaming [llm.Provider] for the bridge
// tests. Each call to Complete pops the next scripted step and streams it as
// EventText / EventToolCall deltas followed by EventDone. It records every
// Request so the test can assert the loop fed tool results back. Keyless — no
// live API.
type scriptedProvider struct {
	mu    sync.Mutex
	steps []step
	reqs  []llm.Request
	next  int
}

// step is one scripted generation: some text and/or some tool calls.
type step struct {
	text  string
	calls []llm.ToolCall
	stop  string
}

func (p *scriptedProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	var s step
	if p.next < len(p.steps) {
		s = p.steps[p.next]
		p.next++
	} else {
		s = step{stop: "end_turn"}
	}
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(s.text) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		for _, c := range s.calls {
			ch <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: c}
		}
		stop := s.stop
		if stop == "" {
			stop = "end_turn"
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: stop}
	}()
	return ch, nil
}

func (p *scriptedProvider) requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

// diceGrants builds a GrantSet granting the real dice Tool, with a seeded rng
// so the roll is deterministic.
func diceGrants(t *testing.T) *tool.GrantSet {
	t.Helper()
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDiceWithRand(rand.New(rand.NewPCG(1, 2))))
	return tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
}

// TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText is the integration
// pin (ADR-0021: tool-call routing is the most important thing to verify): the
// model asks for the dice Tool, the bridge runs it via tool.Loop, feeds the
// result back, and the model's second generation returns the final spoken text.
func TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		// Round 1: the model requests a d20 roll.
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		// Round 2: with the result in context, it answers.
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}

	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The engine returns text verbatim; trimming is the Replier's job.
	if strings.TrimSpace(got) != "You rolled well, traveler." {
		t.Errorf("final text = %q, want the second-round answer", got)
	}

	// The loop must have made two generations, and the second must carry the
	// tool result fed back as a RoleTool message.
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (request → tool result → answer)", len(reqs))
	}
	// First request advertised the dice tool.
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("first request tools = %+v, want one dice decl", reqs[0].Tools)
	}
	// Second request carries the assistant tool_use turn and a RoleTool result.
	var sawAssistantCall, sawToolResult bool
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == "dice" {
			sawAssistantCall = true
		}
		if m.Role == llm.RoleTool {
			for _, tr := range m.ToolResults {
				if tr.CallID == "toolu_1" && tr.Content != "" && !tr.IsError {
					sawToolResult = true
				}
			}
		}
	}
	if !sawAssistantCall {
		t.Error("second request missing the assistant tool_use turn")
	}
	if !sawToolResult {
		t.Error("second request missing the fed-back dice tool result")
	}
}

// llmRound is one captured LLMRound span: the labels the metric carries.
type llmRound struct {
	provider    observe.Provider
	roundIndex  int
	hadToolCall bool
}

// recordingStage is a [observe.StageRecorder] that captures LLMRound and
// provider-call invocations so the A3 instrumentation can be asserted without a
// Prometheus backend. Embeds observe.Discard so the unexercised methods are
// no-ops. Safe for the loop's single-goroutine use plus the race detector.
type recordingStage struct {
	observe.Discard
	mu       sync.Mutex
	rounds   []llmRound
	callsOK  int
	callsErr int
}

func (r *recordingStage) LLMRound(p observe.Provider, idx int, hadToolCall bool, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, llmRound{provider: p, roundIndex: idx, hadToolCall: hadToolCall})
}

func (r *recordingStage) ProviderCall(_ observe.Stage, _ observe.Provider, outcome observe.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if outcome == observe.OutcomeOK {
		r.callsOK++
	} else {
		r.callsErr++
	}
}

func (r *recordingStage) snapshot() ([]llmRound, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llmRound(nil), r.rounds...), r.callsOK, r.callsErr
}

// TestEngine_LLMRoundSpans_IndexAndToolCallPerRound pins the A3 per-round
// instrumentation: each Provider.Complete inside the tool loop emits one
// LLMRound span with a 0-based round_index and the had_tool_call flag that
// separates H1 thinking from H2 extra rounds. The dice round-trip is exactly
// two rounds: round 0 requests the tool (had_tool_call=true), round 1 answers
// (had_tool_call=false).
func TestEngine_LLMRoundSpans_IndexAndToolCallPerRound(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderAnthropic))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans, want 2 (request + answer)", len(rounds))
	}
	if rounds[0].roundIndex != 0 || !rounds[0].hadToolCall {
		t.Errorf("round 0 = %+v, want index 0 with had_tool_call=true", rounds[0])
	}
	if rounds[1].roundIndex != 1 || rounds[1].hadToolCall {
		t.Errorf("round 1 = %+v, want index 1 with had_tool_call=false", rounds[1])
	}
	if rounds[0].provider != observe.ProviderAnthropic {
		t.Errorf("round provider = %q, want the injected label", rounds[0].provider)
	}
	if callsOK != 2 || callsErr != 0 {
		t.Errorf("provider calls ok=%d err=%d, want 2/0", callsOK, callsErr)
	}
}

// TestEngine_LLMRoundSpans_IndexResetsPerTurn pins that round_index is scoped to
// one turn: a second Generate on the same Engine starts again at 0, never
// continuing the previous turn's count (the per-turn ctx counter, not a shared
// adapter field — which would bleed across the concurrent turns barge-in
// allows).
func TestEngine_LLMRoundSpans_IndexResetsPerTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "First answer.", stop: "end_turn"},
		{text: "Second answer.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	for i := 0; i < 2; i++ {
		if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}}); err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
	}

	rounds, _, _ := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d spans, want 2 (one per turn)", len(rounds))
	}
	for i, r := range rounds {
		if r.roundIndex != 0 {
			t.Errorf("turn %d round_index = %d, want 0 (per-turn reset)", i, r.roundIndex)
		}
	}
}

// TestEngine_GenerateStream_ForwardsFinalTextAndKeepsMetrics is the streaming
// twin of the round-span test and the regression guard the streaming production
// path needs: GenerateStream must (a) forward the final answer's prose to onText
// and (b) record the SAME A3 LLMRound / provider-call spans Generate does — the
// streaming path must not silently drop the instrumentation bench-llm keys C1 on.
// A dice round-trip: round 0 requests the tool (no prose), round 1 answers.
func TestEngine_GenerateStream_ForwardsFinalTextAndKeepsMetrics(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	var streamed strings.Builder
	full, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}},
		func(delta string) error { streamed.WriteString(delta); return nil },
	)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if strings.TrimSpace(full) != "You rolled well, traveler." {
		t.Errorf("full = %q, want the final answer", full)
	}
	// The tool-call round emits no prose, so onText only ever sees the answer.
	if strings.TrimSpace(streamed.String()) != "You rolled well, traveler." {
		t.Errorf("streamed = %q, want only the final answer's prose (no tool-round preamble)", streamed.String())
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans on the streaming path, want 2", len(rounds))
	}
	if rounds[0].roundIndex != 0 || !rounds[0].hadToolCall {
		t.Errorf("round 0 = %+v, want index 0 had_tool_call=true", rounds[0])
	}
	if rounds[1].roundIndex != 1 || rounds[1].hadToolCall {
		t.Errorf("round 1 = %+v, want index 1 had_tool_call=false", rounds[1])
	}
	if callsOK != 2 || callsErr != 0 {
		t.Errorf("streaming provider calls ok=%d err=%d, want 2/0", callsOK, callsErr)
	}
}

// TestEngine_GenerateStream_NonStreamingProviderFallsBack pins the fallback: a
// provider that is not a StreamingProvider still works through GenerateStream —
// it runs to completion and forwards the whole text once. (scriptedProvider IS
// wrapped into a streaming adapter, so this exercises the loop's own fallback by
// asserting the single forward equals the full answer for a no-tool turn.)
func TestEngine_GenerateStream_NoToolTurnStreamsAnswer(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Welcome to the inn.", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "claude-test", 256, 0)

	var got strings.Builder
	full, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Hello."}},
		func(delta string) error { got.WriteString(delta); return nil },
	)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if strings.TrimSpace(full) != "Welcome to the inn." || strings.TrimSpace(got.String()) != "Welcome to the inn." {
		t.Errorf("full=%q streamed=%q, want both = the answer", full, got.String())
	}
}

// TestEngine_NoTools_SingleGenerationReturnsText pins that the same bridge path
// covers the no-tool case: with empty grants the loop does one Generate and
// returns the text, so an Agent with no Tool Grants behaves like the default
// provider engine.
func TestEngine_NoTools_SingleGenerationReturnsText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Welcome to the inn.", stop: "end_turn"},
	}}
	emptyGrants := tool.NewGrantSet(tool.NewRegistry())

	eng := agenttool.NewEngine(prov, emptyGrants, "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Hello."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "Welcome to the inn." {
		t.Errorf("text = %q, want single-generation answer", got)
	}
	if n := len(prov.requests()); n != 1 {
		t.Errorf("provider called %d times, want 1 (no tools → one generation)", n)
	}
}

// TestEngine_AsAgentEngine_DrivesReplier pins that the bridge slots into the
// agent loop via agent.Config.Engine — the production wiring path — so an
// addressed utterance flows utterance → Hot Context → tool loop → spoken reply.
func TestEngine_AsAgentEngine_DrivesReplier(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Aye, two rooms upstairs.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "claude-test", 256, 0)

	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart."},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
	got := r.Reply()(t.Context(), addressed("bart", "Any rooms free?"))
	if len(got) != 1 || strings.TrimSpace(got[0].Sentence) != "Aye, two rooms upstairs." {
		t.Fatalf("reply = %+v, want one spoken sentence from the tool engine", got)
	}
	// The Replier assembled Hot Context (system prompt) before the engine ran.
	reqs := prov.requests()
	if len(reqs) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Errorf("engine did not receive an assembled system prompt; first req = %+v", reqs)
	}
}

// truncatingProvider streams some text and closes without [llm.EventDone] — a
// mid-stream failure as the [llm.Provider] contract describes it.
type truncatingProvider struct{}

func (truncatingProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: "Half a sen"}
	close(ch)
	return ch, nil
}

// TestEngine_TruncatedStream_ReturnsError pins the truncation contract on the
// tool-loop bridge: a stream that ends without EventDone must fail the
// generation, never hand the loop (and ultimately TTS) a partial answer.
func TestEngine_TruncatedStream_ReturnsError(t *testing.T) {
	reg := tool.NewRegistry()
	grants := tool.NewGrantSet(reg)
	eng := agenttool.NewEngine(truncatingProvider{}, grants, "m", 0, 0)

	_, err := eng.Generate(t.Context(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "without done") {
		t.Fatalf("Generate on truncated stream = %v, want truncation error", err)
	}
}

// errorEventProvider terminates the stream with an [llm.EventError].
type errorEventProvider struct{}

func (errorEventProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: "Half a sen"}
	ch <- llm.StreamEvent{Type: llm.EventError, Err: "provider: read stream: connection reset"}
	close(ch)
	return ch, nil
}

// TestEngine_EventError_PropagatesAsError pins that a terminal EventError
// surfaces as the generation error, carrying the provider's message.
func TestEngine_EventError_PropagatesAsError(t *testing.T) {
	reg := tool.NewRegistry()
	grants := tool.NewGrantSet(reg)
	eng := agenttool.NewEngine(errorEventProvider{}, grants, "m", 0, 0)

	_, err := eng.Generate(t.Context(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Generate on EventError = %v, want the provider's error", err)
	}
}
