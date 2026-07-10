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
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
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

// FactsRecaller fills the KG-facts slot of Hot Context (ADR-0008 v1.5 / #126): the
// gm-public Knowledge Graph Node facts the Agent's Campaign wants injected into its
// system prompt, each element an already-rendered fact string. It shares the
// [MemoryRecaller] contract: it NEVER errors, respects ctx (a barge-in yields nil),
// and degrades to nil rather than stalling the turn. A nil FactsRecaller (the
// unconfigured default) leaves the slot empty, so the prompt is byte-identical to
// the pre-facts path.
type FactsRecaller interface {
	Facts(ctx context.Context, agentID string) []string
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

	// Facts fills the reserved KG-facts slot of the system prompt each turn (#126,
	// ADR-0008): the Campaign's gm-public Knowledge Graph Node facts. Consulted
	// under the turn ctx alongside Memory, OUTSIDE the history lock, never blocking
	// the turn: a slow/unavailable path degrades to nil. nil disables facts — the
	// slot stays empty and the prompt is byte-identical to the pre-facts behavior.
	Facts FactsRecaller

	// TextSink, when non-nil, gives this Agent a text-delivery channel for its
	// replies (the Butler's in-voice text answers, #299 / #297 decision 2). With a
	// TextSink installed, the streaming [Replier.ReplyStream] path runs a BATCH
	// completion (it needs the whole answer to decide modality), then routes the
	// answer by [AnswerAsText]: a text-modality answer is posted whole via TextSink
	// and committed to history with ZERO TTS dispatch, while a spoken one is
	// sentence-split and dispatched as usual. A nil TextSink (every Character NPC,
	// and the Butler until wired) is byte-identical to the pre-#299 streaming path.
	// TextSink is called under the turn ctx, so a barge cancels a mid-post; its
	// error aborts the turn without committing.
	TextSink func(ctx context.Context, text string) error

	// Retry is the [retry.Policy] the default [providerEngine] wraps its LLM start
	// call in (#124, ADR-0044): a transient 429/5xx or net.Error start-error is
	// retried with backoff INSIDE the per-turn deadline, a non-retryable error fails
	// fast, and a barge cutting ctx aborts at once. Used ONLY by the default engine
	// — a custom [Config.Engine] (the tool-backed bridge) carries its own policy. The
	// zero value is a valid retries-on policy (defaults), so the retry is on unless a
	// caller narrows it.
	Retry retry.Policy
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
		engine = providerEngine{provider: cfg.Provider, model: cfg.Model, maxTokens: cfg.MaxTokens, retry: cfg.Retry}
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

	// Recall + facts run BETWEEN the two lock sections, never under r.mu: they are
	// network-/DB-adjacent and must not hold the loop's lock across the call
	// (ADR-0042). Both respect ctx, so a barge cancels them.
	mem := r.recall(ctx, text)
	facts := r.facts(ctx)

	r.mu.Lock()
	messages := r.hotContextLocked(mem, facts)
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

// Draft produces the Agent's would-be reply text for one utterance WITHOUT
// mutating any state — the speculative fan-out half of an Ensemble Turn (ADR-0025,
// #301). It assembles the SAME Hot Context [Replier.turn] would (system prompt +
// bounded recent Transcript + the user message), but on a SNAPSHOT copy of the
// history, so a candidate that LOSES the Lead race commits nothing: no user
// message appended, no assistant message recorded (ADR-0012's zero-commit rule,
// made structural). An empty completion returns "", nil (the Agent says nothing);
// an engine error — including a ctx cancel when the loser's shared draft context is
// cut after the winner is elected — returns "", err. The winning candidate's
// [Replier.SpeakDraft] is what actually commits the turn.
//
// It honors ctx (bounded by [Config.TurnTimeout] like the LLM turn) and consults
// Memory/Facts under it, so a barge tearing down the whole ensemble unwinds every
// in-flight draft.
func (r *Replier) Draft(ctx context.Context, text string) (string, error) {
	ctx, cancel := r.withTurnTimeout(ctx)
	defer cancel()

	// Snapshot the history and append the user message on the COPY — Draft must not
	// touch the loop's live history (purity).
	r.mu.Lock()
	history := append(make([]llm.Message, 0, len(r.history)+1), r.history...)
	r.mu.Unlock()
	history = append(history, llm.Message{Role: llm.RoleUser, Text: text})
	history = trimHistory(history, r.cfg.HistoryTurns)

	// Recall + facts run under ctx, never mutating loop state (they never did).
	mem := r.recall(ctx, text)
	facts := r.facts(ctx)

	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt(mem, facts)})
	msgs = append(msgs, history...)

	reply, err := r.engine.Generate(ctx, msgs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(reply), nil
}

// SpeakDraft speaks an already-generated draft (the winning Lead's, from a prior
// [Replier.Draft]) as this Agent's turn (ADR-0025, #301). It appends the user
// message (parity with [Replier.streamTurn], which always records the utterance it
// answered), sentence-splits draft, and dispatches each sentence in order,
// committing to history ONLY the text actually delivered (ADR-0012). dispatch is
// the deliver-then-commit signal: a sentence joins the committed text only once
// dispatch returns nil (fully synthesized under a live turn ctx); a dispatch
// reporting the turn cancelled (a barge cutting the ensemble mid-draft) stops the
// drain, and the sentences forwarded before the cut are committed while the rest
// are dropped. It returns the delivered text; a ctx-cancel is the expected barge
// path, not a failure, so it returns a nil error there.
func (r *Replier) SpeakDraft(ctx context.Context, userText, draft string, dispatch func(orchestrator.Reply) error) (delivered string, err error) {
	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleUser, Text: userText})
	r.trimHistoryLocked()
	r.mu.Unlock()

	var split sentenceSplitter
	var spoken strings.Builder
	voice := r.cfg.Persona.Voice

	// Deliver-then-commit (ADR-0012, mirrors streamTurn's emit): dispatch FIRST; a
	// sentence joins the spoken builder only once dispatch returns nil.
	emit := func(sentence string) error {
		if e := dispatch(orchestrator.Reply{Sentence: sentence, Voice: voice}); e != nil {
			return e
		}
		if spoken.Len() > 0 {
			spoken.WriteByte(' ')
		}
		spoken.WriteString(sentence)
		return nil
	}

	var dispErr error
	for _, sentence := range split.Push(draft) {
		if e := emit(sentence); e != nil {
			dispErr = e
			break
		}
	}
	// Flush the trailing unterminated sentence — unless the turn was cut, in which
	// case its tail was never spoken and must not be dispatched.
	if dispErr == nil && ctx.Err() == nil {
		if tail := split.Flush(); tail != "" {
			if e := emit(tail); e != nil {
				dispErr = e
			}
		}
	}

	r.commitSpoken(spoken.String())

	// A cancel is the expected barge path (the whole ensemble was torn down), not a
	// turn failure; surface only a genuine dispatch error under a live ctx.
	if ctx.Err() != nil {
		return spoken.String(), nil
	}
	return spoken.String(), dispErr
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

	// Recall + facts between the lock sections (see [Replier.turn]): outside r.mu,
	// under ctx, each degrading to nothing rather than stalling the streaming turn.
	mem := r.recall(ctx, text)
	facts := r.facts(ctx)

	r.mu.Lock()
	messages := r.hotContextLocked(mem, facts)
	r.mu.Unlock()

	// TextSink installed (the Butler, #299): decide modality on the WHOLE answer, so
	// this path needs a batch completion rather than incremental streaming. A
	// text-modality answer posts via the sink with zero TTS dispatch; a spoken one
	// is sentence-split and dispatched. See [Replier.textModalityTurn].
	if r.cfg.TextSink != nil {
		return r.textModalityTurn(ctx, text, messages, dispatch)
	}

	streamer, ok := r.engine.(StreamingEngine)
	if !ok {
		// Non-streaming engine: fall back to one completion, then dispatch it whole.
		return r.fallbackTurn(ctx, messages, dispatch)
	}

	var split sentenceSplitter
	var spoken strings.Builder // what actually reached the pump — the history commit
	voice := r.cfg.Persona.Voice

	// Deliver-then-commit (ADR-0012): dispatch FIRST; a sentence joins the
	// history commit only once dispatch returns nil, i.e. it was fully
	// synthesized under a live turn ctx. A dispatch rejected because the turn was
	// cancelled between the two select-ready branches (mute/barge) must NOT reach
	// the spoken builder — the room never heard it.
	emit := func(sentence string) error {
		if err := dispatch(orchestrator.Reply{Sentence: sentence, Voice: voice}); err != nil {
			return err
		}
		if spoken.Len() > 0 {
			spoken.WriteByte(' ')
		}
		spoken.WriteString(sentence)
		return nil
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
	// Deliver-then-commit (ADR-0012): dispatch FIRST, commit only once the reply
	// was delivered. A turn cancelled before the reply drained delivered nothing,
	// so it must leave no assistant message.
	if err := dispatch(orchestrator.Reply{Sentence: reply, Voice: r.cfg.Persona.Voice}); err != nil {
		return err
	}
	r.commitSpoken(reply)
	return nil
}

// textModalityTurn runs a Butler turn with a TextSink installed (#299): it takes
// ONE batch completion (the whole answer is needed to decide modality), then
// routes it by [AnswerAsText]. A text-modality answer (voiceless Butler, an
// explicit modality request, or a long result) is posted whole through TextSink
// and committed to history (ADR-0012: text-delivered commits) with NO TTS
// dispatch; a spoken one is sentence-split and dispatched exactly like the batch
// fallback. utterance is the addressed text, consulted for keyword overrides.
//
// Deliver-then-commit (ADR-0012) holds on both branches: the answer is committed
// only after the sink post (text) or the dispatch (voice) succeeds, so a
// barge/cancel that aborts delivery leaves no phantom assistant message.
func (r *Replier) textModalityTurn(ctx context.Context, utterance string, messages []llm.Message, dispatch func(orchestrator.Reply) error) error {
	answer, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if ctx.Err() == nil && r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		return err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}

	voiceless := r.cfg.Persona.Voice.VoiceID == ""
	if AnswerAsText(utterance, answer, voiceless) {
		// Text delivery: post the whole answer to the channel chat, then commit it.
		if err := r.cfg.TextSink(ctx, answer); err != nil {
			return err
		}
		r.commitSpoken(answer)
		return nil
	}

	// Spoken delivery: sentence-split the whole answer and dispatch each, committing
	// only what was delivered (mirrors [Replier.streamTurn]'s emit).
	var split sentenceSplitter
	var spoken strings.Builder
	voice := r.cfg.Persona.Voice
	emit := func(sentence string) error {
		if err := dispatch(orchestrator.Reply{Sentence: sentence, Voice: voice}); err != nil {
			return err
		}
		if spoken.Len() > 0 {
			spoken.WriteByte(' ')
		}
		spoken.WriteString(sentence)
		return nil
	}
	for _, sentence := range split.Push(answer) {
		if err := emit(sentence); err != nil {
			r.commitSpoken(spoken.String())
			return err
		}
	}
	if tail := split.Flush(); tail != "" {
		if err := emit(tail); err != nil {
			r.commitSpoken(spoken.String())
			return err
		}
	}
	r.commitSpoken(spoken.String())
	return nil
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

	// retry wraps the LLM start call (#124, ADR-0044): a transient start-error is
	// retried with backoff, a non-retryable one fails fast, and a barge cutting ctx
	// aborts at once. Only the start is retried — a mid-stream truncation below is
	// never re-driven. Zero value is a valid retries-on policy.
	retry retry.Policy
}

// Generate implements [Engine]. It runs one completion and returns the
// accumulated assistant text. A stream that ends without an [llm.EventDone] —
// an [llm.EventError], a ctx cancellation, or a silent truncation — is an
// error: a partial sentence must never be spoken as the Agent's full reply.
func (e providerEngine) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	// Retry a transient START failure (429/5xx/net) with backoff before draining
	// (#124, ADR-0044); a non-retryable error fails fast and a barge cutting ctx
	// aborts at once, bounded by the per-turn deadline. Only the start is retried —
	// the mid-stream truncation guard below is never re-driven (re-speak risk).
	stream, err := retry.Do(ctx, e.retry, func(ctx context.Context) (<-chan llm.StreamEvent, error) {
		return e.provider.Complete(ctx, llm.Request{
			Model:     e.model,
			MaxTokens: e.maxTokens,
			Messages:  messages,
		})
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

// facts consults the configured [FactsRecaller] for this turn's Hot Context
// KG-facts, keyed by the Agent's id. A nil recaller (the unconfigured default)
// returns no facts, so the reserved slot stays empty and the prompt is
// byte-identical to the pre-facts path (#126). The recaller never errors and
// respects ctx.
func (r *Replier) facts(ctx context.Context) []string {
	if r.cfg.Facts == nil {
		return nil
	}
	return r.cfg.Facts.Facts(ctx, r.cfg.Persona.AgentID)
}

// hotContextLocked assembles the Hot Context message list for one call: the
// system prompt (Persona + KG facts + recalled memory + audio-markup instruction)
// followed by the recent Transcript (the bounded history). mem is the memory
// recalled for this turn and facts are the KG-facts (each nil/zero when
// unconfigured or degraded). Caller holds r.mu.
func (r *Replier) hotContextLocked(mem Memory, facts []string) []llm.Message {
	msgs := make([]llm.Message, 0, len(r.history)+1)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt(mem, facts)})
	msgs = append(msgs, r.history...)
	return msgs
}

// systemPrompt builds the system prompt from the Hot Context inputs in slot order:
// the Persona, the KG-facts block (the reserved facts slot, ADR-0008/#126), the
// recalled memory block (ADR-0011/0042/0012 — both touch the SYSTEM prompt only),
// and the Voice's provider-specific audio-markup instruction from
// [tts.Synthesizer.AudioMarkupPrompt] (required by ADR-0022). Empty facts AND a
// zero mem omit their blocks entirely, leaving the prompt byte-identical to the
// pre-facts/pre-memory path (#126 / AC6).
func (r *Replier) systemPrompt(mem Memory, facts []string) string {
	var b strings.Builder
	if p := strings.TrimSpace(r.cfg.Persona.Markdown); p != "" {
		b.WriteString(p)
	}
	if block := factsBlock(facts); block != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
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

// factsBlock renders the KG-facts slot as a flat-text prompt section: a fixed
// header followed by the already-rendered fact strings joined by blank lines. The
// agent is a dumb joiner — the fact strings arrive fully formatted from the
// FactsRecaller (#126). No facts yields "" so the block is dropped entirely
// (the byte-identical guarantee).
func factsBlock(facts []string) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What you know about the world")
	b.WriteString("\n\n")
	b.WriteString(strings.Join(facts, "\n\n"))
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
	r.history = trimHistory(r.history, r.cfg.HistoryTurns)
}

// trimHistory keeps at most the last keep user/assistant messages of history,
// re-slicing onto a fresh backing array when it trims (so the dropped turns can be
// GC'd and the retained slice does not pin the whole conversation). A zero or
// negative keep means unbounded, and a history already within the bound is returned
// unchanged (same backing) — so the live [Replier.trimHistoryLocked] path is
// byte-for-byte identical while [Replier.Draft] can trim a history COPY too.
func trimHistory(history []llm.Message, keep int) []llm.Message {
	if keep <= 0 || len(history) <= keep {
		return history
	}
	trimmed := make([]llm.Message, keep)
	copy(trimmed, history[len(history)-keep:])
	return trimmed
}
