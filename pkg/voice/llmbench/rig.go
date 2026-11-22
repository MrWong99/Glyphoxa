package llmbench

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// language is the Campaign Language of the bench campaign.
const language = "de"

// historyTurns mirrors the live wiring (internal/wirenpc: 16).
const historyTurns = 16

// persona is Flo's Persona, written the way a GM writes one: who she is and how
// she talks. Nothing in it mentions tools, formats or models — everything of
// that kind comes from the production prompt assembly around it.
const persona = `Du bist Flo, die Wirtin der Taverne „Zur Goldenen Gans“ in Brückenau. Du bist Mitte vierzig, herzlich, schlagfertig und ein bisschen neugierig.

Du redest, wie man am Tresen redet: locker, direkt, in kurzen Sätzen, meistens ein bis drei. Stammgäste nennst du gern „mein Lieber“ oder „meine Liebe“, je nachdem, wen du vor dir hast. Du sprichst immer Deutsch.

Zu deinen Stammgästen gehören die Bardin Mira und der Zwergenkrieger Brak. Du kennst das Dorf und seine Leute. Wenn du etwas nicht sicher weißt, schaust du nach, statt zu raten. Wenn dich jemand bittet zu würfeln, würfelst du wirklich. Was Gäste dir anvertrauen und du dir merken sollst, merkst du dir.`

// speakerNames resolves the bench speaker ids to the display names the
// production prompt prefixes user lines with.
var speakerNames = map[string]string{SpeakerMira: "Mira", SpeakerBrak: "Brak"}

// ownNode is the grant scope of a Character NPC's knowledge Tools: her own
// node and its neighbourhood.
var ownNode = json.RawMessage(`{"scope":"own_node"}`)

// ToolCall is one tool call a turn made.
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// SchemaValid reports whether Input satisfies the Tool's declared schema;
	// SchemaError says why not.
	SchemaValid bool   `json:"schema_valid"`
	SchemaError string `json:"schema_error,omitempty"`
}

// TurnResult is everything recorded about one turn of one candidate.
type TurnResult struct {
	Script  string   `json:"script"`
	Index   int      `json:"index"`
	Speaker string   `json:"speaker"`
	User    string   `json:"user"`
	Tags    []string `json:"tags"`

	// Reply is what would be spoken: the sentences the production path
	// dispatched to TTS, scrubbed. Raw is every text delta the model streamed,
	// over all rounds, before any scrub.
	Reply string `json:"reply"`
	Raw   string `json:"raw"`

	// Timings in milliseconds from the start of the turn; -1 when the event
	// never happened. FirstDelta is the first text delta the provider streamed
	// (in any round); FirstSentence the first complete sentence handed to TTS —
	// what the voice actually waits for; End the end of the turn.
	FirstDeltaMS    float64 `json:"first_delta_ms"`
	FirstSentenceMS float64 `json:"first_sentence_ms"`
	EndMS           float64 `json:"end_ms"`

	// PreToolText is the prose the model wrote in rounds that also called a
	// Tool. On the streaming path it is spoken before the Tool has run — the
	// channel through which a model narrates or reasons aloud ("let me look
	// that up", "I need to roll two dice").
	PreToolText string `json:"pre_tool_text,omitempty"`

	// Rounds is the number of provider completions the turn took.
	Rounds    int        `json:"rounds"`
	ToolCalls []ToolCall `json:"tool_calls"`
	// ToolErrors counts tool results fed back to the model as errors.
	ToolErrors int      `json:"tool_errors"`
	Expect     []string `json:"expect_tools"`
	Allow      []string `json:"allow_tools"`
	// ToolsOK: every expected Tool was called and no unexpected one.
	ToolsOK bool `json:"tools_ok"`

	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`

	// SpokenFindings are the detectors over Reply; RawFindings over Raw.
	SpokenFindings Findings `json:"spoken_findings"`
	RawFindings    Findings `json:"raw_findings"`
	// Malformed counts the production path's own malformed-generation signals
	// for this turn, by detection site (stream_error, text_leak, roll_claim,
	// regen_mismatch).
	Malformed map[string]int `json:"malformed,omitempty"`

	// Fallback is set when the turn failed and the canned stall was spoken
	// instead of a reply; Error carries the failure.
	Fallback bool   `json:"fallback"`
	Error    string `json:"error,omitempty"`
}

// capture is what the provider tap and the recorder collect during one turn.
type capture struct {
	mu         sync.Mutex
	start      time.Time
	firstDelta time.Time
	raw        strings.Builder
	rounds     int
	calls      []llm.ToolCall
	toolErrors int
	in, out    int
	malformed  map[string]int
	preTool    []string // prose of the rounds that also called a Tool
}

// tap wraps the candidate's provider and records each completion of the turn
// in flight: when its first text delta arrived, the raw text, the tool calls
// and the reported usage. It forwards every event unchanged.
type tap struct {
	inner llm.Provider
	mu    sync.Mutex
	cur   *capture
}

func (t *tap) current() *capture {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

func (t *tap) begin() *capture {
	c := &capture{start: time.Now(), malformed: map[string]int{}}
	t.mu.Lock()
	t.cur = c
	t.mu.Unlock()
	return c
}

// Complete implements [llm.Provider].
func (t *tap) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	c := t.current()
	if c != nil {
		c.mu.Lock()
		c.rounds++
		// The tool results of the previous round ride this request.
		if last := req.Messages[len(req.Messages)-1]; last.Role == llm.RoleTool {
			for _, r := range last.ToolResults {
				if r.IsError {
					c.toolErrors++
				}
			}
		}
		c.mu.Unlock()
	}

	in, err := t.inner.Complete(ctx, req)
	if err != nil || c == nil {
		return in, err
	}
	out := make(chan llm.StreamEvent)
	go func() {
		defer close(out)
		var roundText strings.Builder
		roundCalled := false
		defer func() {
			if text := strings.TrimSpace(roundText.String()); roundCalled && text != "" {
				c.mu.Lock()
				c.preTool = append(c.preTool, text)
				c.mu.Unlock()
			}
		}()
		for ev := range in {
			c.mu.Lock()
			switch ev.Type {
			case llm.EventText:
				if c.firstDelta.IsZero() && ev.Text != "" {
					c.firstDelta = time.Now()
				}
				c.raw.WriteString(ev.Text)
				roundText.WriteString(ev.Text)
			case llm.EventToolCall:
				roundCalled = true
				c.calls = append(c.calls, ev.ToolCall)
			case llm.EventUsage:
				c.in += ev.Usage.InputTokens
				c.out += ev.Usage.OutputTokens
			case llm.EventDone:
				c.raw.WriteString("\n")
			}
			c.mu.Unlock()
			out <- ev
		}
	}()
	return out, nil
}

// malformedRecorder is the [observe.StageRecorder] the engine reports to; it
// keeps only the malformed-generation counter, on the turn in flight.
type malformedRecorder struct {
	observe.Discard
	tap *tap
}

func (m malformedRecorder) MalformedToolGen(_ observe.Provider, path observe.MalformedPath) {
	if c := m.tap.current(); c != nil {
		c.mu.Lock()
		c.malformed[string(path)]++
		c.mu.Unlock()
	}
}

// session is one candidate's conversation for one script: a production Replier
// over the candidate's provider, with its own history.
type session struct {
	cand   Candidate
	script string
	tap    *tap
	reg    *tool.Registry
	reply  orchestrator.StreamReplyFunc

	mu      sync.Mutex
	lastErr error
}

// newSession wires the production reply path for cand: the real prompt
// assembly and sentence delivery of [agent.Replier], the real tool loop, dice
// gate, invented-roll guard and scrubber of [agenttool.Engine], and the real
// provider adapter — everything a live voice turn runs between the transcribed
// utterance and the sentence handed to TTS, and nothing after it.
func newSession(cand Candidate, script string, turnTimeout time.Duration) *session {
	s := &session{cand: cand, script: script, tap: &tap{inner: cand.New()}}

	w := &world{}
	s.reg = tool.BuiltinRegistry(w.deps())
	grants := tool.NewGrantSet(s.reg,
		tool.Grant{ToolName: toolDice},
		tool.Grant{ToolName: toolRecap},
		tool.Grant{ToolName: toolSearch},
		tool.Grant{ToolName: toolKG, Config: ownNode},
		tool.Grant{ToolName: toolRemember, Config: ownNode},
	)
	engine := agenttool.NewEngine(s.tap, grants, agentID, cand.Model, 0, 0,
		agenttool.WithMetrics(malformedRecorder{tap: s.tap}, cand.Provider),
		agenttool.WithLanguage(language),
	)

	// The voice only shapes the prompt (the spoken-output rule and the TTS
	// markup instruction); nothing is ever synthesized.
	voice := ttseleven.DefaultVoice("bench-voice", language)
	voice.Name = npcName
	replier := agent.NewReplier(agent.Config{
		Persona:           agent.Persona{AgentID: agentID, Name: npcName, Markdown: persona, Voice: voice},
		Engine:            engine,
		Synthesizer:       ttseleven.New("unused"),
		HistoryTurns:      historyTurns,
		SpeakerName:       func(id string) string { return speakerNames[id] },
		PlayerCharacters:  []string{speakerNames[SpeakerMira], speakerNames[SpeakerBrak]},
		FallbackLine:      agent.FallbackLineFor(language),
		SentenceIdleFlush: agent.DefaultSentenceIdleFlush,
		TurnTimeout:       turnTimeout,
		OnError: func(err error) {
			s.mu.Lock()
			s.lastErr = err
			s.mu.Unlock()
		},
	})
	s.reply = replier.ReplyStream()
	return s
}

// run plays one turn and returns its record.
func (s *session) run(ctx context.Context, index int, turn Turn) TurnResult {
	res := TurnResult{
		Script: s.script, Index: index, Speaker: speakerNames[turn.Speaker], User: turn.Text, Tags: turn.Tags,
		Expect: turn.Expect, Allow: turn.Allow,
		FirstDeltaMS: -1, FirstSentenceMS: -1,
	}
	s.mu.Lock()
	s.lastErr = nil
	s.mu.Unlock()

	c := s.tap.begin()
	var sentences []string
	var firstSentence time.Time
	err := s.reply(ctx, voiceevent.AddressRouted{
		At:        c.start,
		Text:      turn.Text,
		Target:    voiceevent.AddressTarget{AgentID: agentID},
		SpeakerID: turn.Speaker,
	}, func(r orchestrator.Reply) error {
		if firstSentence.IsZero() {
			firstSentence = time.Now()
		}
		sentences = append(sentences, r.Sentence)
		return nil // delivered: the Replier commits it to history
	})
	end := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	res.Reply = strings.Join(sentences, " ")
	res.Raw = strings.TrimSpace(c.raw.String())
	res.EndMS = ms(end.Sub(c.start))
	if !c.firstDelta.IsZero() {
		res.FirstDeltaMS = ms(c.firstDelta.Sub(c.start))
	}
	if !firstSentence.IsZero() {
		res.FirstSentenceMS = ms(firstSentence.Sub(c.start))
	}
	res.Rounds = c.rounds
	res.PreToolText = strings.Join(c.preTool, " ")
	res.ToolErrors = c.toolErrors
	res.InputTokens, res.OutputTokens = c.in, c.out
	res.CostUSD = spend.EstimateLLMUSD(s.cand.Provider, s.cand.Model, c.in, c.out)
	if len(c.malformed) > 0 {
		res.Malformed = c.malformed
	}

	var called []string
	for _, call := range c.calls {
		tc := ToolCall{Name: call.Name, Input: call.Input}
		if t, ok := s.reg.Get(call.Name); !ok {
			tc.SchemaError = "no such tool"
		} else if verr := ValidateInput(t.InputSchema(), call.Input); verr != nil {
			tc.SchemaError = verr.Error()
		} else {
			tc.SchemaValid = true
		}
		res.ToolCalls = append(res.ToolCalls, tc)
		called = append(called, call.Name)
	}
	res.ToolsOK = turn.ToolsOK(called)

	s.mu.Lock()
	turnErr := s.lastErr
	s.mu.Unlock()
	if turnErr == nil {
		turnErr = err
	}
	if turnErr != nil {
		res.Error = turnErr.Error()
		res.Fallback = res.Reply == agent.FallbackLineFor(language)
	}

	opts := DetectOptions{NPCName: npcName, FemaleSpeaker: slices.Contains(turn.Tags, TagFemalePC)}
	if !res.Fallback {
		res.SpokenFindings = Detect(res.Reply, opts)
	}
	res.RawFindings = Detect(res.Raw, opts)
	return res
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Runner plays the scripts against the candidates.
type Runner struct {
	Candidates []Candidate
	Scripts    []Script
	// Delay is the pause before every turn, to stay clear of rate limits.
	Delay time.Duration
	// Sleep performs the pause; nil sleeps for real (ctx-aware).
	Sleep func(ctx context.Context, d time.Duration)
	// TurnTimeout bounds one turn's generation; zero is the production default.
	TurnTimeout time.Duration
	// Logf, when set, receives one progress line per turn.
	Logf func(format string, args ...any)
}

// Run plays every script against every candidate and returns one report per
// candidate, in table order.
//
// The candidates are interleaved at turn granularity — turn 1 for every
// candidate, then turn 2, … — and the order within a turn rotates, so every
// candidate is measured across the same stretch of wall-clock time and none
// always goes first. Each (candidate, script) pair has its own Replier, so a
// script's history never crosses candidates.
func (r Runner) Run(ctx context.Context) []CandidateReport {
	sleep := r.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}

	reports := make([]CandidateReport, len(r.Candidates))
	for i, c := range r.Candidates {
		reports[i].Candidate = info(c)
	}

	n := len(r.Candidates)
	step := 0
	for _, script := range r.Scripts {
		sessions := make([]*session, n)
		for i, c := range r.Candidates {
			sessions[i] = newSession(c, script.Name, r.TurnTimeout)
		}
		for ti, turn := range script.Turns {
			for k := 0; k < n; k++ {
				// Rotate who goes first, so no candidate always leads a turn.
				i := (k + step) % n
				if ctx.Err() != nil {
					break
				}
				sleep(ctx, r.Delay)
				res := sessions[i].run(ctx, ti, turn)
				reports[i].Turns = append(reports[i].Turns, res)
				if r.Logf != nil {
					r.Logf("%-24s %s[%d] sentence=%.0fms end=%.0fms rounds=%d tools=%v ok=%v err=%q",
						r.Candidates[i].Name, script.Name, ti, res.FirstSentenceMS, res.EndMS, res.Rounds, toolNames(res.ToolCalls), res.ToolsOK, res.Error)
				}
			}
			step++
		}
	}
	for i := range reports {
		reports[i].Summary = Summarize(reports[i].Turns)
	}
	return reports
}

func toolNames(calls []ToolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Name
	}
	return out
}
