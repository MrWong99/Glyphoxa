package agenttool_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
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
	mu           sync.Mutex
	rounds       []llmRound
	callsOK      int
	callsErr     int
	turns        []observe.Provider // one LLMTurn span per Engine.Generate/GenerateStream
	outcomes     []observe.Outcome  // every ProviderCall outcome, in order
	providerErrs int                // ProviderError invocations
}

func (r *recordingStage) LLMRound(p observe.Provider, idx int, hadToolCall bool, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, llmRound{provider: p, roundIndex: idx, hadToolCall: hadToolCall})
}

func (r *recordingStage) LLMTurn(p observe.Provider, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turns = append(r.turns, p)
}

func (r *recordingStage) turnSpans() []observe.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.Provider(nil), r.turns...)
}

func (r *recordingStage) ProviderCall(_ observe.Stage, _ observe.Provider, outcome observe.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
	if outcome == observe.OutcomeOK {
		r.callsOK++
	} else {
		r.callsErr++
	}
}

func (r *recordingStage) ProviderError(observe.Stage, observe.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerErrs++
}

func (r *recordingStage) snapshot() ([]llmRound, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llmRound(nil), r.rounds...), r.callsOK, r.callsErr
}

func (r *recordingStage) providerCalls() ([]observe.Outcome, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.Outcome(nil), r.outcomes...), r.providerErrs
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

// flakyStartProvider start-errors its first errsBeforeOK Complete calls (each the
// pinned err) then streams the answer, counting calls so a retry test can prove
// the loop re-drove the LLM start exactly as expected. Only the START is exercised
// — the retry contract is start-errors-only (#124, ADR-0044).
type flakyStartProvider struct {
	err          error
	errsBeforeOK int
	answer       string
	calls        int
}

func (p *flakyStartProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	p.calls++
	if p.calls <= p.errsBeforeOK {
		return nil, p.err
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(p.answer) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// instantAgentRetry is the deterministic retry policy for the bridge tests: the
// backoff is a no-op sleep with fixed jitter, so a retry never sleeps wall-clock
// (ADR-0021). Defaults (3 attempts) otherwise stand.
func instantAgentRetry() retry.Policy {
	return retry.Policy{
		Sleep: func(context.Context, time.Duration) error { return nil },
		Rand:  func() float64 { return 0 },
	}
}

// TestEngine_Generate_RetriesTransientStartError pins the LLM AC: a Complete that
// start-errors one 429 then succeeds returns the full answer, and metrics record
// the FINAL outcome only — one LLMRound span and one provider_call(ok), never one
// per attempt (ADR-0044). No provider_error, because the retry recovered.
func TestEngine_Generate_RetriesTransientStartError(t *testing.T) {
	prov := &flakyStartProvider{
		err:          &providererr.HTTPError{Op: "groq.Complete", StatusCode: 429, Status: "Too Many Requests", Body: "slow"},
		errsBeforeOK: 1,
		answer:       "Aye, traveler.",
	}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}})
	if err != nil {
		t.Fatalf("Generate after one transient 429: %v", err)
	}
	if strings.TrimSpace(got) != "Aye, traveler." {
		t.Errorf("final text = %q, want the answer after the retry", got)
	}
	if prov.calls != 2 {
		t.Errorf("provider Complete calls = %d, want 2 (one 429 retried once)", prov.calls)
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 1 {
		t.Errorf("LLMRound spans = %d, want 1 (final round only, not one per attempt)", len(rounds))
	}
	if callsOK != 1 || callsErr != 0 {
		t.Errorf("provider calls ok=%d err=%d, want 1/0 (final outcome only)", callsOK, callsErr)
	}
	if _, provErrs := rec.providerCalls(); provErrs != 0 {
		t.Errorf("provider_errors = %d, want 0 (the retry recovered)", provErrs)
	}
}

// TestEngine_Generate_NonRetryableStartErrorFailsFast pins that a 400 (bad
// request / auth) start-error fails on the first attempt with no retry and no
// sleep, and is recorded as a single provider fault.
func TestEngine_Generate_NonRetryableStartErrorFailsFast(t *testing.T) {
	prov := &flakyStartProvider{
		err:          &providererr.HTTPError{Op: "groq.Complete", StatusCode: 400, Status: "Bad Request", Body: "nope"},
		errsBeforeOK: 99,
	}
	rec := &recordingStage{}
	p := instantAgentRetry()
	p.Sleep = func(context.Context, time.Duration) error {
		t.Error("a non-retryable 400 must not sleep")
		return nil
	}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(p))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}}); err == nil {
		t.Fatal("Generate returned nil on a 400; want the bad-request error")
	}
	if prov.calls != 1 {
		t.Errorf("provider Complete calls = %d, want 1 (a 400 is not retried)", prov.calls)
	}

	_, callsOK, callsErr := rec.snapshot()
	if callsOK != 0 || callsErr != 1 {
		t.Errorf("provider calls ok=%d err=%d, want 0/1", callsOK, callsErr)
	}
	if _, provErrs := rec.providerCalls(); provErrs != 1 {
		t.Errorf("provider_errors = %d, want 1 (a 400 start error is a fault)", provErrs)
	}
}

// TestEngine_LLMTurn_OneSpanPerTurnAcrossRounds pins the #125 llm_turn wiring:
// the Engine records exactly ONE LLMTurn span per Generate — the full agenttool
// loop, spanning both rounds of a dice round-trip — labelled with the injected
// provider, distinct from the per-round LLMRound spans (two here).
func TestEngine_LLMTurn_OneSpanPerTurnAcrossRounds(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	rounds, _, _ := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans, want 2 (round-trip)", len(rounds))
	}
	turns := rec.turnSpans()
	if len(turns) != 1 {
		t.Fatalf("recorded %d LLMTurn spans, want exactly 1 per turn (across all rounds)", len(turns))
	}
	if turns[0] != observe.ProviderGroq {
		t.Errorf("LLMTurn provider = %q, want the injected groq label", turns[0])
	}
}

// TestEngine_GenerateStream_RecordsOneLLMTurn pins that the streaming production
// path records the SAME single LLMTurn span Generate does — the metric must not be
// dropped by the streaming loop.
func TestEngine_GenerateStream_RecordsOneLLMTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}},
		func(string) error { return nil },
	); err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	if turns := rec.turnSpans(); len(turns) != 1 {
		t.Fatalf("streaming recorded %d LLMTurn spans, want exactly 1", len(turns))
	}
}

// TestEngine_LLMTurn_RecordsOnError pins that a turn whose loop fails still records
// its LLMTurn span — the full-turn latency series must see failed turns too, not
// just successful ones (otherwise a provider outage silently drops the samples).
func TestEngine_LLMTurn_RecordsOnError(t *testing.T) {
	rec := &recordingStage{}
	eng := agenttool.NewEngine(errorEventProvider{}, tool.NewGrantSet(tool.NewRegistry()), "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}); err == nil {
		t.Fatal("Generate on a failing provider returned nil error")
	}
	if turns := rec.turnSpans(); len(turns) != 1 {
		t.Fatalf("recorded %d LLMTurn spans on the error path, want exactly 1", len(turns))
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

// TestEngine_DiceGate_PlainTurnOffersNoDiceAndIsSingleRound is the core #5 pin:
// a conversational utterance with no dice intent must NOT declare the dice Tool,
// so the model cannot emit an empty tool-call round before its answer — the turn
// is structurally a single LLM round. This is the baseline-latency win.
func TestEngine_DiceGate_PlainTurnOffersNoDiceAndIsSingleRound(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ale's a copper, traveler.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)

	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "How much for a room and a pint?"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "Ale's a copper, traveler." {
		t.Errorf("text = %q, want the single-round answer", got)
	}
	reqs := prov.requests()
	if len(reqs) != 1 {
		t.Fatalf("provider called %d times, want 1 (a plain turn must be one round)", len(reqs))
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("plain turn declared tools %+v, want none (dice gated out)", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_KeywordIntentOffersDice proves the gate's recall: an
// utterance with ttrpg roll intent but no explicit NdM (a "saving throw") still
// arms the dice Tool, so the tool path is not broken when dice IS needed.
func TestEngine_DiceGate_KeywordIntentOffersDice(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You steady your nerves.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Make a saving throw against the poison."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (dice intent → tool round-trip)", len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("keyword-intent turn tools = %+v, want the dice decl offered", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_HistoryDiceDoesNotArmCurrentTurn pins that the gate keys on
// the CURRENT utterance only: a dice roll three messages ago must not keep dice
// declared on a later plain turn.
func TestEngine_DiceGate_HistoryDiceDoesNotArmCurrentTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Right this way.", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20."},                // older turn: had dice intent
		{Role: llm.RoleAssistant, Text: "You rolled a 14."},      // its answer
		{Role: llm.RoleUser, Text: "Great, point me to a room."}, // current turn: plain
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("history dice armed the current plain turn: reqs=%d tools=%+v, want 1 req / 0 tools", len(reqs), reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanUtteranceOffersDiceWithLanguage is the #226 pin: a
// German campaign (WithLanguage("de")) arms the dice Tool for the exact live
// failure utterance — „Würfelwerkzeug … würfle zwei sechsseitige Würfel" — so
// the model gets a real tool round-trip instead of improvising the roll.
func TestEngine_DiceGate_GermanUtteranceOffersDiceWithLanguage(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":2,"sides":6}`)}}, stop: "tool_use"},
		{text: "Eine Vier und eine Zwei.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithLanguage("de-DE"))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Bart, benutze dein Würfelwerkzeug und würfle zwei sechsseitige Würfel."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (German dice intent → tool round-trip)", len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("German dice turn tools = %+v, want the dice decl offered", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanPlainTurnGatedWithLanguage proves the other #226
// direction: with WithLanguage("de") the German article „die" no longer trips
// the gate, so a plain German turn stays a single dice-less round.
func TestEngine_DiceGate_GermanPlainTurnGatedWithLanguage(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Es war einmal…", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0,
		agenttool.WithLanguage("de"))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Erzähl mir die Geschichte von diesem Ort."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("plain German turn: reqs=%d tools=%+v, want 1 req / 0 tools (article „die\" gated)", len(reqs), reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanUtteranceWithoutLanguageStaysEnglish pins that the
// language must be threaded to fix #226: without WithLanguage the gate stays
// English (the pre-#226 behavior, byte-for-byte), so the same German dice
// utterance is gated out — reproducing the live false negative.
func TestEngine_DiceGate_GermanUtteranceWithoutLanguageStaysEnglish(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "role-played roll", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Bart, benutze dein Würfelwerkzeug und würfle zwei sechsseitige Würfel."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("German utterance under the EN default: reqs=%d tools=%+v, want 1 req / 0 tools (dice gated — the #226 failure)", len(reqs), reqs[0].Tools)
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

// startErrProvider fails the Complete call itself (start error, no stream), the
// shape a 4xx/5xx takes.
type startErrProvider struct{}

func (startErrProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, errors.New("provider: start boom")
}

// bargeStartProvider blocks the Complete call until the ctx is cancelled, then
// returns ctx.Err() — a barge-in that cuts the dial in flight (a pre-cancelled ctx
// would instead make the tool loop bail before it ever calls the provider).
type bargeStartProvider struct{}

func (bargeStartProvider) Complete(ctx context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestEngine_StartError_ClassifiesOutcomeLikeStages pins the #239 alignment: the
// LLM start-error path classifies via the shared observe.CallOutcome rule, so a
// barge cancelling the dial in flight is outcome=canceled with NO provider_error
// (not a fault), while a live-ctx start failure is outcome=error WITH a
// provider_error. This keeps the LLM stage in agreement with STT/TTS.
func TestEngine_StartError_ClassifiesOutcomeLikeStages(t *testing.T) {
	empty := tool.NewGrantSet(tool.NewRegistry())

	t.Run("barge cancel is canceled, not a fault", func(t *testing.T) {
		rec := &recordingStage{}
		eng := agenttool.NewEngine(bargeStartProvider{}, empty, "m", 0, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := eng.Generate(ctx, []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
			done <- err
		}()
		time.Sleep(20 * time.Millisecond) // let the loop reach the blocked Complete
		cancel()                          // barge-in cuts the dial

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Generate returned nil on a cancelled dial")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Generate did not return after the ctx was cancelled")
		}
		outcomes, provErrs := rec.providerCalls()
		if len(outcomes) != 1 || outcomes[0] != observe.OutcomeCanceled {
			t.Errorf("provider_call outcomes = %v, want [canceled]", outcomes)
		}
		if provErrs != 0 {
			t.Errorf("provider_errors = %d on a barge cancel, want 0 (not a fault)", provErrs)
		}
	})

	t.Run("live-ctx start failure is error + fault", func(t *testing.T) {
		rec := &recordingStage{}
		eng := agenttool.NewEngine(startErrProvider{}, empty, "m", 0, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))
		if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}); err == nil {
			t.Fatal("Generate returned nil on a start failure")
		}
		outcomes, provErrs := rec.providerCalls()
		if len(outcomes) != 1 || outcomes[0] != observe.OutcomeError {
			t.Errorf("provider_call outcomes = %v, want [error]", outcomes)
		}
		if provErrs != 1 {
			t.Errorf("provider_errors = %d on a vendor start failure, want 1", provErrs)
		}
	})
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
