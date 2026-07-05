// Package agent assembles an Agent's spoken turn: the production
// [orchestrator.ReplyFunc] the voice pipeline stubs with a cassette in tests.
//
// Per ADR-0019 the production ReplyFunc is "the Agent loop" — Hot Context
// assembly (recent Transcript + KG-facts placeholder + Persona) plus the LLM
// dispatch. This package builds that loop on top of [llm.Provider] and a
// [tts.Synthesizer] (for the Voice's audio-markup instruction), and exposes it
// as a ReplyFunc consumable by [orchestrator.WithReply].
//
// Tool-use seam (ADR-0028): the loop does ONE LLM call per turn in v1.0 and
// returns the spoken text. It does not execute tools — that is the tool-use
// loop owned by the tool framework. The seam is the [llm] message vocabulary:
// the [Replier] holds the running conversation as a []llm.Message, which is
// exactly where the tool-use loop will later insert assistant tool_use turns
// and tool-role result turns between this turn's user message and the final
// assistant reply.
package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Persona is the Markdown description of an Agent's personality, backstory, and
// speech style injected into the LLM system prompt (CONTEXT.md "Persona"),
// paired with the AgentID this loop answers for and the Voice its replies are
// spoken with.
//
// The loop answers only routes whose [voiceevent.AddressTarget.AgentID] equals
// AgentID; a route for any other Agent yields no reply, so several Agents'
// loops can share one bus and each speak only when addressed (the Ensemble
// Turn building block, ADR-0025).
type Persona struct {
	// AgentID is the stable Agent identifier this loop answers for, matched
	// against [voiceevent.AddressTarget.AgentID].
	AgentID string

	// Markdown is the Persona text injected verbatim into the system prompt.
	Markdown string

	// Voice selects the TTS voice the replies are rendered with and is the
	// Voice handed to [tts.Synthesizer.AudioMarkupPrompt] when building the
	// system prompt, so the LLM learns the provider-appropriate markup syntax.
	Voice tts.Voice
}

// Engine turns the Hot Context the [Replier] assembled into the Agent's final
// spoken text. It is the seam where tool use plugs in without the agent package
// learning about Tools: the default engine ([providerEngine]) makes one
// [llm.Provider] completion and returns the streamed text; a tool-backed engine
// (the wiring-phase bridge that owns both [llm] and pkg/tool) instead drives
// the tool-use loop (ADR-0028) — Generate, execute granted Tools, feed results
// back, Generate again — and returns the model's final text. Either way the
// Replier's contract is unchanged: assemble messages, get one answer back.
//
// messages is the assembled conversation (system prompt + bounded recent
// Transcript). The returned string is the spoken reply; "" means say nothing.
type Engine interface {
	Generate(ctx context.Context, messages []llm.Message) (string, error)
}

// StreamingEngine is the optional streaming extension of [Engine] (B1): an
// Engine that can implement it surfaces the final answer's text incrementally,
// calling onText with each delta as it streams, so the [Replier] can segment the
// text into sentences and dispatch them to TTS before the whole completion is
// done. The tool-backed engine implements this by streaming the final
// (no-tool-call) round; tool-call rounds happen first and produce no spoken text.
//
// GenerateStream returns the full accumulated answer text (for the history /
// fallback). onText is called on the calling goroutine, in order; an error it
// returns (a barge-in cancel surfaced through the dispatch callback) aborts
// generation promptly. A nil onText is treated as a plain [Engine.Generate].
// When an Engine does not implement this, the Replier falls back to the batch
// path and segments the complete text after the fact.
type StreamingEngine interface {
	Engine
	GenerateStream(ctx context.Context, messages []llm.Message, onText func(delta string) error) (full string, err error)
}

// Memory is the recalled Hot Context memory slot (ADR-0011/0042): the Transcript
// Chunks an Agent may reference this turn, split by retrieval mode. Personal is
// NPC-knowledge — chunks the Agent personally participated in ("witnessed") —
// while World is campaign-wide topical context the Agent may not have been present
// for, framed as possibly second-hand (ADR-0011). Each string is one chunk's
// content. A zero Memory (both empty) injects nothing, leaving the prompt
// byte-identical to the no-memory path.
type Memory struct {
	// Personal is NPC-knowledge: chunks the Agent participated in.
	Personal []string
	// World is campaign-wide context — framed "may be second-hand" (ADR-0011).
	World []string
}

// IsZero reports whether the Memory carries no chunks in either mode — the
// signal to omit the whole memory block from the system prompt.
func (m Memory) IsZero() bool { return len(m.Personal) == 0 && len(m.World) == 0 }

// MemoryRecaller retrieves the Hot Context [Memory] for one turn: the chunks the
// Agent (agentID) may reference given the current utterance. It NEVER returns an
// error — degradation (an embeddings/DB timeout or an unavailable provider)
// yields a zero Memory rather than stalling the turn (ADR-0042). Implementations
// respect ctx: a barge-in cancels retrieval and yields zero Memory.
type MemoryRecaller interface {
	Recall(ctx context.Context, agentID, utterance string) Memory
}

// Config configures a [Replier].
type Config struct {
	// Persona is the Agent this loop voices.
	Persona Persona

	// Provider is the LLM the default [Engine] calls. Required unless a custom
	// Engine is supplied; with a custom Engine, Provider may be nil.
	Provider llm.Provider

	// Engine turns assembled Hot Context into final spoken text. Optional: when
	// nil, the Replier builds the default single-completion engine from
	// Provider (the no-tool v1.0 path). Supply a tool-backed Engine (built by
	// the wiring bridge) to give the Agent Tool use without this package
	// importing pkg/tool.
	Engine Engine

	// Synthesizer supplies the audio-markup instruction for the system prompt
	// via [tts.Synthesizer.AudioMarkupPrompt]. Required: ADR-0022 makes the
	// Persona/LLM layer responsible for emitting text in the Voice's provider
	// format, and that instruction is sourced here.
	Synthesizer tts.Synthesizer

	// Model overrides the Provider's default model for this Agent. Used only by
	// the default Engine; a custom Engine bakes its own model in. Optional.
	Model string

	// MaxTokens caps each completion. Used only by the default Engine. Zero
	// lets the Provider choose. Optional.
	MaxTokens int

	// HistoryTurns bounds the recent Transcript carried into Hot Context: the
	// last HistoryTurns user/assistant messages are kept, older ones dropped.
	// Zero means unbounded (keep the whole conversation). This is the recent-
	// Transcript half of Hot Context (CONTEXT.md "Hot Context") — the loop owns
	// it because a [orchestrator.ReplyFunc] is handed only the current
	// utterance, not the history.
	HistoryTurns int

	// OnError reports an LLM failure. A [orchestrator.ReplyFunc] cannot return
	// an error (it runs inside a bus callback), so a failed completion returns
	// no reply and is surfaced here. A nil OnError drops the error silently.
	OnError func(error)

	// TurnTimeout bounds one turn's LLM work (including any tool-use rounds a
	// tool-backed Engine runs). The turn's context is cancelled when it elapses,
	// unwinding the in-flight provider call, and the turn yields no reply (the
	// deadline error goes to OnError). Zero applies [DefaultTurnTimeout];
	// negative disables the deadline (the turn is still cancellable via the
	// caller's ctx, e.g. barge-in).
	TurnTimeout time.Duration

	// Memory recalls the Hot Context memory chunks injected into the system prompt
	// each turn (ADR-0011/0042). It is consulted under the turn ctx, OUTSIDE the
	// history lock (it is network-adjacent), and never blocks the turn: a slow or
	// unavailable path degrades to a zero Memory. nil disables recall entirely —
	// the prompt is then byte-identical to the pre-memory behavior (AC6).
	Memory MemoryRecaller
}

// DefaultTurnTimeout is the per-turn LLM deadline applied when
// [Config.TurnTimeout] is zero. A voice turn that takes this long is dead in
// conversational terms anyway; the deadline exists so a hung provider can never
// wedge the reply path forever.
const DefaultTurnTimeout = 60 * time.Second

// Replier is the stateful Agent loop. Each addressed utterance appends a user
// message and the assistant's reply to a running conversation, so the recent
// Transcript lives in the loop rather than in the per-utterance
// [voiceevent.AddressRouted] (which carries only the current text). Construct
// with [NewReplier]; obtain the [orchestrator.ReplyFunc] with [Replier.Reply].
//
// Safe for sequential turns. The bus delivers [voiceevent.AddressRouted]
// synchronously in one goroutine (see [voiceevent.Bus]), so turns do not
// overlap; the mutex guards the history against a concurrent reader and the
// race detector.
type Replier struct {
	cfg    Config
	engine Engine // resolved at construction: cfg.Engine, or the default built from cfg.Provider

	mu      sync.Mutex
	history []llm.Message // user/assistant turns only; system prompt is rebuilt each call
}

// NewReplier constructs a [Replier]. cfg.Synthesizer must be non-nil, and one
// of cfg.Engine or cfg.Provider must be set (Engine wins; otherwise the default
// single-completion engine is built from Provider). Passing neither, or a nil
// Synthesizer, panics — these are wiring requirements, mirroring the
// orchestrator stages' fail-fast constructors.
func NewReplier(cfg Config) *Replier {
	if cfg.Synthesizer == nil {
		panic("agent.NewReplier: Synthesizer must not be nil")
	}
	engine := cfg.Engine
	if engine == nil {
		if cfg.Provider == nil {
			panic("agent.NewReplier: one of Engine or Provider must be set")
		}
		engine = providerEngine{provider: cfg.Provider, model: cfg.Model, maxTokens: cfg.MaxTokens}
	}
	return &Replier{cfg: cfg, engine: engine}
}

// Reply returns the [orchestrator.ReplyFunc] that drives this loop. Install it
// with [orchestrator.WithReply]. The returned closure runs synchronously inside
// the reply reactor's bus callback (ADR-0026): it assembles Hot Context, makes
// one blocking LLM call, and returns the spoken reply. On a route for a
// different Agent it returns nil (says nothing); on an LLM failure it returns
// nil and reports via [Config.OnError].
//
// The turn runs under ctx — with barge-in wired, the per-turn floor context,
// so a barge cancels the LLM call itself — further bounded by
// [Config.TurnTimeout] so a hung provider cannot hold the turn open forever.
func (r *Replier) Reply() orchestrator.ReplyFunc {
	return func(ctx context.Context, e voiceevent.AddressRouted) []orchestrator.Reply {
		if e.Target.AgentID != r.cfg.Persona.AgentID {
			return nil // not addressed to this Agent
		}
		ctx, cancel := r.withTurnTimeout(ctx)
		defer cancel()
		return r.turn(ctx, e.Text)
	}
}

// withTurnTimeout bounds one turn's work with [Config.TurnTimeout] (zero applies
// [DefaultTurnTimeout]; negative disables the deadline). It also substitutes a
// background context for a nil one so a turn always has a valid parent. The
// returned cancel must be called when the turn ends (defer) — it releases the
// timer and, on the disabled-deadline path, is the no-op cancel of the
// passed-through context.
//
// Both the batch ([Replier.Reply]) and streaming ([Replier.ReplyStream]) entry
// points go through here so the per-turn deadline is identical on both — the
// streaming path is the one production wires ([orchestrator.WithReplyStream]),
// and the Gemini adapter's HTTP client has no overall timeout by design,
// relying on exactly this ctx deadline to bound a thinking-then-stalling
// completion (gemini.defaultHTTPClient).
func (r *Replier) withTurnTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := r.cfg.TurnTimeout
	if timeout == 0 {
		timeout = DefaultTurnTimeout
	}
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// turn runs one Agent turn for the given utterance text: it appends the user
// message, assembles Hot Context (system prompt + bounded history), hands it to
// the [Engine] for the final text, records the assistant reply, and returns it
// as a single [orchestrator.Reply] in the Agent's Voice. An empty completion or
// an Engine error yields no reply (the error is reported via OnError).
func (r *Replier) turn(ctx context.Context, text string) []orchestrator.Reply {
	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleUser, Text: text})
	r.trimHistoryLocked()
	r.mu.Unlock()

	// Recall runs BETWEEN the two lock sections, never under r.mu: it is
	// network-adjacent (embeddings + DB) and must not hold the loop's lock across
	// that call (ADR-0042). It respects ctx, so a barge cancels it.
	mem := r.recall(ctx, text)

	r.mu.Lock()
	messages := r.hotContextLocked(mem)
	r.mu.Unlock()

	reply, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		return nil
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}

	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleAssistant, Text: reply})
	r.mu.Unlock()

	return []orchestrator.Reply{{Sentence: reply, Voice: r.cfg.Persona.Voice}}
}

// ReplyStream returns the [orchestrator.StreamReplyFunc] that drives this loop
// in streaming mode (B1): it dispatches each sentence of the reply to TTS the
// moment it is ready, so first audio begins after the first sentence rather than
// the whole completion. Install it with [orchestrator.WithReplyStream].
//
// It requires the configured [Engine] to implement [StreamingEngine]; if it does
// not, every turn falls back to a single post-completion dispatch (the behaviour
// of [Replier.Reply]) so the wiring is always safe, just not incremental.
func (r *Replier) ReplyStream() orchestrator.StreamReplyFunc {
	return func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		if e.Target.AgentID != r.cfg.Persona.AgentID {
			return nil // not addressed to this Agent
		}
		// Bound the streaming turn with the same per-turn deadline as the batch
		// path: production wires this path, and without it a thinking-then-stalling
		// provider completion runs unbounded (the Gemini client has no overall HTTP
		// timeout by design), so a wedged turn would never produce first audio.
		ctx, cancel := r.withTurnTimeout(ctx)
		defer cancel()
		return r.streamTurn(ctx, e.Text, dispatch)
	}
}

// streamTurn runs one streaming Agent turn: it assembles Hot Context, drives the
// [StreamingEngine] over ctx, segments the streamed text into sentences, and
// hands each to dispatch as it completes. ctx is the per-turn context, so a
// barge-in cancel both ends the LLM generation and stops further dispatch.
//
// History (ADR-0012, deliver-then-commit): only the text actually emitted to the
// pump is committed to the conversation history. A turn cut mid-stream by a
// barge-in records what Bart already said, not the untruncated completion he
// would have said — so the next turn's Hot Context reflects what the user heard.
func (r *Replier) streamTurn(ctx context.Context, text string, dispatch func(orchestrator.Reply) error) error {
	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleUser, Text: text})
	r.trimHistoryLocked()
	r.mu.Unlock()

	// Recall between the lock sections (see [Replier.turn]): outside r.mu, under
	// ctx, degrading to zero Memory rather than stalling the streaming turn.
	mem := r.recall(ctx, text)

	r.mu.Lock()
	messages := r.hotContextLocked(mem)
	r.mu.Unlock()

	streamer, ok := r.engine.(StreamingEngine)
	if !ok {
		// Non-streaming engine: fall back to one completion, then dispatch it whole.
		return r.fallbackTurn(ctx, messages, dispatch)
	}

	var split sentenceSplitter
	var spoken strings.Builder // what actually reached the pump — the history commit
	voice := r.cfg.Persona.Voice

	emit := func(sentence string) error {
		if spoken.Len() > 0 {
			spoken.WriteByte(' ')
		}
		spoken.WriteString(sentence)
		return dispatch(orchestrator.Reply{Sentence: sentence, Voice: voice})
	}

	onText := func(delta string) error {
		for _, sentence := range split.Push(delta) {
			if err := emit(sentence); err != nil {
				return err // ctx cancelled (barge-in) or a hard dispatch failure
			}
		}
		return nil
	}

	_, genErr := streamer.GenerateStream(ctx, messages, onText)

	// Flush the trailing unterminated sentence — unless generation was cancelled,
	// in which case the partial tail was never spoken and must not be dispatched.
	if genErr == nil && ctx.Err() == nil {
		if tail := split.Flush(); tail != "" {
			if err := emit(tail); err != nil {
				genErr = err
			}
		}
	}

	r.commitSpoken(spoken.String())

	// A cancellation is the expected barge-in path, not a turn failure: report
	// only a genuine engine error.
	if genErr != nil && ctx.Err() == nil {
		if r.cfg.OnError != nil {
			r.cfg.OnError(genErr)
		}
		return genErr
	}
	return nil
}

// fallbackTurn handles a streaming reply when the engine cannot stream: it runs
// one completion and dispatches the whole reply as a single sentence, mirroring
// the batch [Replier.turn] so a non-streaming engine still speaks.
func (r *Replier) fallbackTurn(ctx context.Context, messages []llm.Message, dispatch func(orchestrator.Reply) error) error {
	reply, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if ctx.Err() == nil && r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		return err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	r.commitSpoken(reply)
	return dispatch(orchestrator.Reply{Sentence: reply, Voice: r.cfg.Persona.Voice})
}

// HistorySnapshot returns a copy of the running conversation history (the recent
// Transcript half of Hot Context) in order. It is a point-in-time read for
// diagnostics and tests — the slice is a copy, so callers may keep it without
// pinning or racing the loop's live history.
func (r *Replier) HistorySnapshot() []llm.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.Message(nil), r.history...)
}

// commitSpoken records the text actually delivered to the pump as the assistant
// turn in history (ADR-0012). An empty string — a turn cancelled before any
// sentence was spoken — records nothing, so a barged-out turn leaves no phantom
// assistant message.
func (r *Replier) commitSpoken(spoken string) {
	spoken = strings.TrimSpace(spoken)
	if spoken == "" {
		return
	}
	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleAssistant, Text: spoken})
	r.mu.Unlock()
}

// providerEngine is the default [Engine]: one [llm.Provider] completion per
// turn, concatenating the streamed text deltas. It requests no Tools, so the
// model emits none and tool-call events do not arise — the no-tool v1.0 path.
// The tool-backed engine lives in the wiring bridge so this package stays free
// of pkg/tool.
type providerEngine struct {
	provider  llm.Provider
	model     string
	maxTokens int
}

// Generate implements [Engine]. It runs one completion and returns the
// accumulated assistant text. A stream that ends without an [llm.EventDone] —
// an [llm.EventError], a ctx cancellation, or a silent truncation — is an
// error: a partial sentence must never be spoken as the Agent's full reply.
func (e providerEngine) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	stream, err := e.provider.Complete(ctx, llm.Request{
		Model:     e.model,
		MaxTokens: e.maxTokens,
		Messages:  messages,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var done bool
	var streamErr error
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			b.WriteString(ev.Text)
		case llm.EventDone:
			done = true
		case llm.EventError:
			streamErr = errors.New(ev.Err)
		}
	}
	if streamErr != nil {
		return "", streamErr
	}
	if !done {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", errors.New("agent: completion stream ended without done event (truncated response)")
	}
	return b.String(), nil
}

// recall consults the configured [MemoryRecaller] for this turn's Hot Context
// memory, keyed by the Agent's id and the current utterance. A nil recaller (the
// unconfigured default) returns a zero Memory, so the prompt stays byte-identical
// to the pre-memory path (AC6). The recaller never errors and respects ctx.
func (r *Replier) recall(ctx context.Context, text string) Memory {
	if r.cfg.Memory == nil {
		return Memory{}
	}
	return r.cfg.Memory.Recall(ctx, r.cfg.Persona.AgentID, text)
}

// hotContextLocked assembles the Hot Context message list for one call: the
// system prompt (Persona + recalled memory + audio-markup instruction) followed
// by the recent Transcript (the bounded history). mem is the memory recalled for
// this turn (zero when unconfigured or degraded). Caller holds r.mu.
func (r *Replier) hotContextLocked(mem Memory) []llm.Message {
	msgs := make([]llm.Message, 0, len(r.history)+1)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt(mem)})
	msgs = append(msgs, r.history...)
	return msgs
}

// systemPrompt builds the system prompt from the three Hot Context inputs in slot
// order: the Persona, the recalled memory block (the reserved Hot Context memory
// slot, ADR-0011/0042/0012 — memory touches the SYSTEM prompt only), and the
// Voice's provider-specific audio-markup instruction from
// [tts.Synthesizer.AudioMarkupPrompt] (required by ADR-0022). A zero mem omits
// the memory block entirely, leaving the prompt byte-identical to today (AC6).
func (r *Replier) systemPrompt(mem Memory) string {
	var b strings.Builder
	if p := strings.TrimSpace(r.cfg.Persona.Markdown); p != "" {
		b.WriteString(p)
	}
	if block := memoryBlock(mem); block != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
	}
	if markup := r.cfg.Synthesizer.AudioMarkupPrompt(r.cfg.Persona.Voice); markup != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(markup)
	}
	return b.String()
}

// memoryBlock renders the recalled [Memory] as a flat-text prompt section that
// distinguishes NPC-knowledge (personally witnessed) from world context. Per
// ADR-0011 world context is "topical, may not personally know" — so it is framed
// as things the Agent may know of but may not have witnessed first-hand, NOT as an
// assertion the Agent was absent (a chunk it participated in but that fell outside
// its NPC-knowledge top-k can still land here). An empty half omits its whole
// subsection; a zero Memory yields "" so the block is dropped entirely.
func memoryBlock(mem Memory) string {
	if mem.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Things you remember from this campaign")
	if len(mem.Personal) > 0 {
		b.WriteString("\n\nYou personally witnessed:")
		for _, c := range mem.Personal {
			b.WriteString("\n- ")
			b.WriteString(c)
		}
	}
	if len(mem.World) > 0 {
		b.WriteString("\n\nYou may know these from around the campaign, though you may not have witnessed them personally:")
		for _, c := range mem.World {
			b.WriteString("\n- ")
			b.WriteString(c)
		}
	}
	return b.String()
}

// trimHistoryLocked drops the oldest user/assistant turns so at most
// HistoryTurns remain, keeping the most recent. A zero or negative
// HistoryTurns means unbounded. Caller holds r.mu.
func (r *Replier) trimHistoryLocked() {
	if r.cfg.HistoryTurns <= 0 || len(r.history) <= r.cfg.HistoryTurns {
		return
	}
	// Re-slice onto a fresh backing array so the dropped turns can be GC'd and
	// the retained slice does not pin the whole conversation.
	keep := r.cfg.HistoryTurns
	trimmed := make([]llm.Message, keep)
	copy(trimmed, r.history[len(r.history)-keep:])
	r.history = trimmed
}
