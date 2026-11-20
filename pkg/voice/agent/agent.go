// Package agent assembles an Agent's spoken turn: the production
// [orchestrator.ReplyFunc] the voice pipeline stubs with a cassette in tests.
//
// Per ADR-0019 the production reply strategy is "the Agent loop" — Hot Context
// assembly plus the LLM dispatch. Hot Context is a session-constant system
// prompt (Persona, table roster, spoken-output and audio-markup rules), the
// bounded recent Transcript, and a volatile tail carrying this turn's KG facts,
// party location, recalled memory and GM directive (ADR-0059). This package
// builds that loop on top of an [Engine] and a [tts.Synthesizer] (for the
// Voice's audio-markup instruction) and exposes it as the streaming
// [orchestrator.StreamReplyFunc] production wires ([Replier.ReplyStream]) and
// its batch twin ([Replier.Reply]); a [Cast] multiplexes several Agents onto
// one conversation.
//
// Tool-use seam (ADR-0028): this package never executes Tools and does not
// import pkg/tool. The [Engine] interface is the seam — the default engine makes
// one [llm.Provider] completion per turn, while the tool-backed engine the
// wiring installs (pkg/voice/agenttool) runs the whole tool-use loop, as many
// LLM rounds as the Tools need, and hands back only the final spoken text. The
// tool_use and tool-result messages of those rounds live inside the engine for
// the duration of the turn; the [Replier]'s history holds user and assistant
// text messages only.
//
// Generation and playback are decoupled on the streaming path: the engine's
// text is drained into a sentence queue as fast as the provider produces it,
// and a dispatcher delivers the queue to TTS at speaking pace. The per-turn
// deadline ([Config.TurnTimeout]) therefore bounds generation, not how long the
// reply takes to say.
package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

	// Name is the Agent's display name — the same string Address Detection
	// matches on and the Transcript Chunker labels this Agent's lines with. It is
	// NOT used for routing here; the Replier needs it to recognize its own name in
	// its OWN output and strip it before speaking (see [stripSelfPrefix]), because
	// Hot Context teaches the model a "Name: text" grammar the model then imitates.
	// Empty disables that strip, which is byte-identical to the pre-#528 behaviour.
	Name string

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
// GenerateStream returns the full accumulated answer text. onText is called on
// the calling goroutine, in order, and never blocks on playback: the Replier
// queues the text and speaks it from another goroutine ([Replier.streamTurn]).
// An error it returns — the turn was cancelled, or the reply overran the
// sentence queue — must abort generation promptly. A nil onText is treated as a
// plain [Engine.Generate].
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
// signal to omit the whole memory block from the volatile Hot Context tail.
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
// prompt, each element an already-rendered fact string. It shares the
// [MemoryRecaller] contract: it NEVER errors, respects ctx (a barge-in yields nil),
// and degrades to nil rather than stalling the turn. A nil FactsRecaller (the
// unconfigured default) leaves the slot empty, so the prompt is byte-identical to
// the pre-facts path.
type FactsRecaller interface {
	Facts(ctx context.Context, agentID string) []string
}

// LocationRecaller fills the party-location slot of the volatile Hot Context tail
// (#540, ADR-0059 note): one short clause naming where the party is, for Agents
// whose linked Node is on the current Map.
//
// Like the other tail recallers it NEVER errors and never stalls the turn — an
// unavailable read yields "" and the block is dropped, leaving the prompt
// byte-identical to the pre-marker path. A nil recaller disables the slot.
type LocationRecaller interface {
	// Location returns the location clause for agentID, or "" when no marker is
	// set, the Agent is not on the current Map, or the marker points at something
	// the table must not be told about.
	Location(ctx context.Context, agentID string) string
}

// DirectiveRecaller fills the GM-directive slot of the volatile Hot Context tail
// (ADR-0059): the private steering note a GM issued for one Agent (the /direct
// Regie-track — "Bart lies about the key"), injected into that Agent's prompt
// without the table ever hearing it. It shares the [MemoryRecaller] contract:
// it NEVER errors, respects ctx, and degrades to "" rather than stalling the
// turn. A nil DirectiveRecaller (the unconfigured default) leaves the slot
// empty, so the prompt is byte-identical to the pre-directive path.
type DirectiveRecaller interface {
	// Directive returns the active directive text for agentID, or "" when none.
	// consume reports whether this consult belongs to a COMMITTED turn (the
	// single-target batch/streaming reply paths): a turn-bounded directive's
	// remaining-turns budget is decremented only then. The speculative Ensemble
	// consults ([Replier.Draft] / [Replier.React]) peek with consume=false so a
	// losing candidate never burns a turn of the budget.
	Directive(ctx context.Context, agentID string, consume bool) string
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

	// HistoryTurns bounds the recent Transcript carried into Hot Context, in
	// user/assistant messages. The window is trimmed in BLOCKS, not one message
	// per append: it grows to twice HistoryTurns and is then cut back to the most
	// recent HistoryTurns, moved forward to start on a user message (see
	// [trimHistory]). A window that slid on every append would change the start
	// of the conversation each turn and defeat the provider's prefix cache
	// (ADR-0059). Zero means unbounded (keep the whole conversation). This is the
	// recent-Transcript half of Hot Context (CONTEXT.md "Hot Context") — the loop
	// owns it because a [orchestrator.ReplyFunc] is handed only the current
	// utterance, not the history.
	HistoryTurns int

	// OnError reports an LLM failure. A [orchestrator.ReplyFunc] cannot return
	// an error (it runs inside a bus callback), so a failed completion returns
	// no reply and is surfaced here. A nil OnError drops the error silently.
	OnError func(error)

	// TurnTimeout bounds one turn's GENERATION: Hot Context recall plus the LLM
	// work (including any tool-use rounds a tool-backed Engine runs). It does not
	// bound playback — on the streaming path the reply is drained from the
	// provider as it is generated and spoken from a queue afterwards, so an
	// answer that takes minutes to say is not cut off by it. When it elapses the
	// in-flight provider call is unwound; the deadline error goes to OnError,
	// whatever was generated before it is still spoken, and a turn that had
	// nothing to say speaks the canned [Config.FallbackLine] (the caller's ctx is
	// still live — a hung provider is a terminal failure, not a barge). Zero
	// applies [DefaultTurnTimeout]; negative disables the deadline (the turn is
	// still cancellable via the caller's ctx, e.g. barge-in).
	TurnTimeout time.Duration

	// Memory recalls the Hot Context memory chunks injected into the volatile tail
	// each turn (ADR-0011/0042). It is consulted under the turn ctx, OUTSIDE the
	// history lock (it is network-adjacent), and never blocks the turn: a slow or
	// unavailable path degrades to a zero Memory. nil disables recall entirely —
	// the prompt is then byte-identical to the pre-memory behavior (AC6).
	Memory MemoryRecaller

	// Facts fills the reserved KG-facts slot of the volatile Hot Context tail each
	// turn (#126, ADR-0008): the Campaign's gm-public Knowledge Graph Node facts.
	// Consulted under the turn ctx alongside Memory, OUTSIDE the history lock,
	// never blocking the turn: a slow/unavailable path degrades to nil. nil
	// disables facts — the slot stays empty and the prompt is byte-identical to
	// the pre-facts behavior.
	Facts FactsRecaller

	// Directive fills the GM-directive slot of the volatile Hot Context tail each
	// turn (ADR-0059): the private /direct steering note for this Agent, rendered
	// LAST in the tail so the freshest instruction carries the strongest recency
	// signal. Consulted under the turn ctx alongside Memory/Facts, OUTSIDE the
	// history lock; the committed reply paths consume a turn of a turn-bounded
	// directive's budget, the speculative Draft/React paths only peek. nil
	// disables the slot — the prompt is byte-identical to the pre-directive path.
	Directive DirectiveRecaller

	// Location fills the party-location slot of the volatile Hot Context tail
	// (#540, ADR-0059 note): one bounded clause naming where the party is. It sits
	// in the tail and NEVER in the stable prefix — the party moves, and a
	// per-turn-changing line in the cache-stable system prompt would break prefix
	// caching on every move. nil disables the slot.
	Location LocationRecaller

	// SpeakerName resolves a route's SpeakerID (the ADR-0050 Speaker Lane
	// attribution) to the human speaker's display name, so the utterance enters
	// the conversation as "<Name>: <text>" — the same "Who: text" language the
	// Transcript Chunker records (#281) — and multiple humans' turns stay
	// distinguishable in Hot Context. It MUST be cache-only and never block (it
	// runs on the turn's hot path; the live binary wires the speaker.Resolver's
	// non-blocking Lookup, warmed on VADSpeechStart). A "" result — resolver
	// miss or unattributed lane — labels the line "Player / DM", matching the
	// relay/chunker fallback. nil disables attribution entirely: the user line
	// is the bare utterance, byte-identical to the pre-seam prompt (the
	// MemoryRecaller seam pattern — pkg/voice stays free of internal/ imports).
	// Memory recall stays keyed on the RAW utterance either way (ADR-0042: the
	// speculative-recall match compares against unprefixed STT text).
	SpeakerName func(speakerID string) string

	// PlayerCharacters are the display names of the Campaign's bound player
	// characters — the humans at the table, named as the SpeakerName resolver
	// labels their user lines. With a SpeakerName resolver wired and at least one
	// of PlayerCharacters/FellowNPCs set, the system prompt appends a short
	// speaker-attribution section (see [Replier.systemPrompt]) explaining the
	// "Name: text" user-line prefix and listing who is human vs. NPC — otherwise
	// the model's persona prior wins over the prefix and it misattributes
	// speakers (an NPC persona hard-coding a relationship reads every line as
	// that person). Unset (with FellowNPCs also unset, or SpeakerName nil) the
	// prompt is byte-identical to the pre-roster path. Plain data, wired like
	// SpeakerName (the MemoryRecaller seam pattern — pkg/voice stays free of
	// internal/ imports); loaded at session start, like the roster itself.
	PlayerCharacters []string

	// FellowNPCs are the display names of the OTHER Agents in the scene (this
	// Persona's name excluded). Listed in the speaker-attribution section so a
	// name collision — an NPC named like a human's display name — cannot be
	// misread: a user-line prefix always names a human, never one of these. See
	// [Config.PlayerCharacters] for the section's gating and compat contract.
	FellowNPCs []string

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

	// SentenceIdleFlush is how long the streaming turn lets a reply sit on a
	// finished-looking sentence before speaking it without waiting for the next
	// delta (see [sentenceFeed]): the stream paused there — a stalled provider, a
	// slow tool round — and what follows is no longer one token away. Zero (the
	// default) or negative turns it off, which makes sentence segmentation a
	// pure function of the deltas with no wall clock in it — what cassette and
	// replay tests need. The live wiring sets [DefaultSentenceIdleFlush].
	SentenceIdleFlush time.Duration

	// FallbackLine is the canned sentence spoken when a turn's Engine fails
	// TERMINALLY (provider outage, truncation, a hang killed by the turn's own
	// [Config.TurnTimeout] deadline, an exhausted tool budget with no prose):
	// after OnError, the turn dispatches this one line in the Persona's Voice so
	// the room hears something instead of dead air, and reports the turn
	// delivered rather than abandoned. It is NEVER committed to history — it is
	// not the model's words. Empty selects [DefaultFallbackLine]; the live wiring
	// passes [FallbackLineFor] the Campaign Language. It is never
	// spoken on a barge/ctx-cancel of the CALLER's turn ctx (a superseded turn
	// stays silent — the turn's own TurnTimeout expiry is a terminal failure,
	// not a barge, and DOES speak), never once a sentence dispatch was ATTEMPTED
	// (the room may already have heard partial audio — a delivered sentence, or
	// a #436 mid-stream TTS failure's fragment), and never for a voiceless
	// Persona (empty VoiceID must not reach TTS). On an Ensemble Turn whose
	// every candidate Draft failed terminally, the coordinator speaks the
	// top-scored candidate's line via [Cast.SpeakFallback].
	FallbackLine string
}

// DefaultTurnTimeout is the per-turn generation deadline applied when
// [Config.TurnTimeout] is zero. A reply that has not finished GENERATING after
// this long is dead in conversational terms; the deadline exists so a hung
// provider can never wedge the reply path forever.
const DefaultTurnTimeout = 60 * time.Second

// DefaultFallbackLine is the spoken fallback applied when [Config.FallbackLine]
// is empty: the in-character stall an Agent utters when its Engine failed
// terminally and there is no reply text to speak.
const DefaultFallbackLine = "Hm... give me a moment, my thoughts wandered."

// fallbackLines are the canned stalls by Campaign Language (primary subtag).
// English is [DefaultFallbackLine].
var fallbackLines = map[string]string{
	"de": "Hm... einen Moment, ich habe gerade den Faden verloren.",
}

// FallbackLineFor returns the canned fallback line for a Campaign Language
// (CONTEXT.md): the line in that language when one is shipped, else
// [DefaultFallbackLine]. language is a BCP 47-style tag; only its primary
// subtag is consulted ("de-AT" → "de"), case-insensitively. The result is what
// the wiring sets as [Config.FallbackLine], so an NPC in a German campaign does
// not stall in English.
func FallbackLineFor(language string) string {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(language)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	if line, ok := fallbackLines[primary]; ok {
		return line
	}
	return DefaultFallbackLine
}

// MaxSpokenRunes caps how much text one streaming turn may queue for speech.
// Until generation was decoupled from playback the per-turn deadline cut every
// reply after about a minute of audio by accident; this is the deliberate bound
// that replaces it. 6000 runes is roughly six to seven minutes of speech — the
// recap Tool legitimately asks for answers of up to about 3500 — so no real
// reply reaches it, while a model that will not stop cannot hold the floor (and
// bill TTS) indefinitely. At the cap generation is stopped and what is queued
// is spoken; see [Replier.streamTurn].
const MaxSpokenRunes = 6000

// sentenceQueueCap is the sentence queue's capacity. A sentence costs at least
// three runes against [MaxSpokenRunes] (a speakable rune, its terminator, the
// separator), so the rune cap always binds first and a push never finds the
// queue full.
const sentenceQueueCap = MaxSpokenRunes/3 + 1

// errReplyTooLong stops the generation of a reply that reached
// [MaxSpokenRunes]. It is a limit, not a failure: the turn reports it through
// OnError and ends cleanly on what was queued.
var errReplyTooLong = errors.New("agent: reply reached the per-turn spoken-text cap; generation stopped")

// Replier is the stateful Agent loop. Each addressed utterance appends a user
// message and the assistant's reply to a running conversation, so the recent
// Transcript lives in the loop rather than in the per-utterance
// [voiceevent.AddressRouted] (which carries only the current text). Construct
// with [NewReplier]; obtain the [orchestrator.ReplyFunc] with [Replier.Reply].
//
// Concurrency: without barge-in the bus delivers [voiceevent.AddressRouted]
// synchronously in one goroutine (see [voiceevent.Bus]), so turns run strictly
// one after another. Under [orchestrator.WithBargeIn] each turn runs on its own
// goroutine, and a superseded turn's unwind may briefly overlap the superseding
// turn: its delivered-prefix commit can land AFTER the superseding turn
// recorded its user message. Every assistant commit is therefore ANCHORED to its
// own turn's user message ([Replier.commitSpoken]) rather than appended at the
// end, so the history always reads user, its reply, next user — a late commit
// can never leave the conversation ending on the previous turn's assistant
// text, which the next generation would read as its own half-finished answer.
// Callers may rely on Floor exclusivity (no two turns of this Agent are ever
// audible at once) and on that ordering.
type Replier struct {
	cfg    Config
	engine Engine // resolved at construction: cfg.Engine, or the default built from cfg.Provider

	mu      sync.Mutex
	history []llm.Message // user/assistant turns only; system prompt is rebuilt each call
	// turnOf[i] is the turn that produced history[i] (kept in lockstep with
	// history); lastTurn is the last id handed out. It is what lets an assistant
	// commit find its own user message — see [Replier.commitSpoken].
	turnOf   []uint64
	lastTurn uint64
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
		// Keep the PRE-timeout parent (#473 review): the fallback gates judge
		// silence by ITS liveness — a barge/supersede cancels the parent (a
		// superseded turn stays silent), while the turn's own TurnTimeout expiry
		// cancels only the child, and a hung provider killed by the deadline is a
		// terminal failure the room should hear the canned stall for.
		parent := ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := r.withTurnTimeout(parent)
		defer cancel()
		return r.turn(ctx, parent, e.SpeakerID, e.Text)
	}
}

// unknownSpeakerLabel is the user-line attribution for a speaker the SpeakerName
// resolver cannot name (cold cache, unmapped speaker, or the unattributed default
// lane) — the same generic-human label the SSE relay and Transcript Chunker use
// (#281), so the prompt and the recorded transcript speak one language.
const unknownSpeakerLabel = "Player / DM"

// userLine renders one human utterance as the user message Hot Context carries:
// "<Name>: <text>" when a [Config.SpeakerName] resolver is wired (the
// transcript-names seam, mirroring the chunker's "Who: text" grain), the bare
// text when it is nil — byte-identical to the pre-seam prompt. The prefix is a
// history/prompt concern ONLY: memory recall and the text-modality decision keep
// keying on the raw utterance.
func (r *Replier) userLine(speakerID, text string) string {
	if r.cfg.SpeakerName == nil {
		return text
	}
	name := r.cfg.SpeakerName(speakerID)
	if name == "" {
		name = unknownSpeakerLabel
	}
	return name + ": " + text
}

// fallbackLine returns the terminal-error utterance: [Config.FallbackLine], or
// [DefaultFallbackLine] when unset.
func (r *Replier) fallbackLine() string {
	if r.cfg.FallbackLine != "" {
		return r.cfg.FallbackLine
	}
	return DefaultFallbackLine
}

// fallbackReply is the batch-path spoken fallback for a terminal Engine error:
// one Reply carrying the canned line in the Persona's Voice, with NO
// OnDelivered hook — the line is not the model's words and never enters
// history. ctx is the caller's PRE-timeout turn ctx (#473 review): a cancelled
// one (barge/supersede) or a voiceless Persona yields nil, preserving the barge
// silence and the voiceless-never-reaches-TTS guarantee — while the turn's own
// TurnTimeout expiry, which cancels only the timeout child, still speaks.
func (r *Replier) fallbackReply(ctx context.Context) []orchestrator.Reply {
	if ctx.Err() != nil || r.cfg.Persona.Voice.VoiceID == "" {
		return nil
	}
	return []orchestrator.Reply{{Sentence: r.fallbackLine(), Voice: r.cfg.Persona.Voice}}
}

// speakFallback dispatches the canned fallback line for a terminal Engine error
// on a dispatch-driven turn, reporting whether it was spoken — false under a
// cancelled ctx (a barged turn stays silent) or for a voiceless Persona (an
// empty VoiceID must never reach TTS), in which case the caller propagates the
// original error. ctx is the caller's PRE-timeout turn ctx (#473 review), so the
// turn's own TurnTimeout expiry — which cancels only the timeout child, and
// whose dispatch runs under the still-live outer turn — still speaks the line.
// Best-effort: a rejected dispatch is not re-driven (the reactor already
// recorded the TTS failure), and the line is never committed to history.
func (r *Replier) speakFallback(ctx context.Context, dispatch func(orchestrator.Reply) error) bool {
	if ctx.Err() != nil || r.cfg.Persona.Voice.VoiceID == "" {
		return false
	}
	_ = dispatch(orchestrator.Reply{Sentence: r.fallbackLine(), Voice: r.cfg.Persona.Voice})
	return true
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
// streaming path is the one production wires ([orchestrator.ReplyStrategy.Stream]),
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
// message (speaker-attributed via [Replier.userLine]), assembles Hot Context
// (system prompt + bounded history), hands it to the [Engine] for the final
// text, records the assistant reply, and returns it as a single
// [orchestrator.Reply] in the Agent's Voice. An empty completion yields no
// reply; an Engine error is reported via OnError and speaks the canned
// fallback. parent is the caller's PRE-timeout turn ctx, the fallback's
// silence gate (#473 review).
func (r *Replier) turn(ctx, parent context.Context, speakerID, text string) []orchestrator.Reply {
	turn := r.appendUser(r.userLine(speakerID, text))

	// The Hot Context recallers run BETWEEN the two lock sections, never under
	// r.mu: they are network-/DB-adjacent and must not hold the loop's lock across
	// the call (ADR-0042). All respect ctx, so a barge cancels them. Recall keys on
	// the RAW utterance, never the attributed line (the speculative-recall match).
	// The directive consult CONSUMES a turn of its budget — this is a committed
	// reply path (ADR-0059).
	in := r.gather(ctx, text, true)

	r.mu.Lock()
	messages := r.hotContextLocked(in)
	r.mu.Unlock()

	reply, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		// Terminal engine error: speak the canned fallback instead of dead air.
		// The gate is the PARENT ctx: nil under a barge cancel (a superseded turn
		// stays silent), spoken when only the turn's own TurnTimeout expired.
		return r.fallbackReply(parent)
	}
	reply = stripSelfPrefix(r.cfg.Persona.Name, strings.TrimSpace(reply))
	if reply == "" {
		return nil
	}

	// Deliver-then-commit (#362, ADR-0012): the assistant turn is NOT appended
	// eagerly. It is committed only when the dispatch site delivers the sentence and
	// invokes OnDelivered — so a batch turn barged before any audio leaves no phantom
	// assistant message (zero delivered → not logged). The user message stays
	// committed eagerly (parity with streamTurn/SpeakDraft). The Agent batch emits ONE
	// Reply (the whole reply), so a barge mid-drain commits nothing.
	return []orchestrator.Reply{{
		Sentence:    reply,
		Voice:       r.cfg.Persona.Voice,
		OnDelivered: func() { r.commitSpoken(turn, reply) },
	}}
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
func (r *Replier) Draft(ctx context.Context, speakerID, text string) (string, error) {
	return r.draftWithLine(ctx, r.userLine(speakerID, text), text)
}

// draftWithLine is [Replier.Draft] with the speaker-attributed user line already
// resolved — the [Cast]'s Ensemble Turn entry point (#473 review): the Cast
// resolves the line ONCE per (turn, Agent) at Draft time and reuses the SAME
// string for the turn's Speak/React/SpeakReaction, so the committed history can
// never drift from the prompt the draft reasoned over when [Config.SpeakerName]'s
// live cache warms (or empties) mid-turn. text stays the RAW utterance — memory
// recall keys on it (ADR-0042), never the attributed line.
func (r *Replier) draftWithLine(ctx context.Context, userLine, text string) (string, error) {
	ctx, cancel := r.withTurnTimeout(ctx)
	defer cancel()

	// Snapshot the history and append the user message on the COPY — Draft must not
	// touch the loop's live history (purity). The candidate reasons over the same
	// speaker-attributed line SpeakDraft would commit, so prompt and record agree.
	r.mu.Lock()
	history := append(make([]llm.Message, 0, len(r.history)+1), r.history...)
	r.mu.Unlock()
	history = append(history, llm.Message{Role: llm.RoleUser, Text: userLine})
	history = trimHistory(history, r.cfg.HistoryTurns)

	// The recallers run under ctx, never mutating loop state (they never did).
	// Recall keys on the RAW utterance (ADR-0042), not the attributed line. The
	// directive consult only PEEKS (consume=false): a draft that loses the Lead
	// race must not burn a turn of the budget (ADR-0059).
	in := r.gather(ctx, text, false)

	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt()})
	msgs = append(msgs, history...)
	msgs = appendVolatileTail(msgs, factsBlock(in.facts), locationBlock(in.location), memoryBlock(in.mem), directiveBlock(in.directive))

	reply, err := r.engine.Generate(ctx, msgs)
	if err != nil {
		return "", err
	}
	return stripSelfPrefix(r.cfg.Persona.Name, strings.TrimSpace(reply)), nil
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
func (r *Replier) SpeakDraft(ctx context.Context, speakerID, userText, draft string, dispatch func(orchestrator.Reply) error) (delivered string, err error) {
	// The committed user message is the speaker-attributed line (the same one
	// Draft reasoned over); the modality decision keys on the RAW utterance.
	return r.speakDraftModality(ctx, r.userLine(speakerID, userText), userText, draft, dispatch)
}

// ReactsAsText reports whether this Agent would deliver its Cross-talk Reaction
// (reaction) to utterance as channel TEXT rather than speech — the SAME
// [AnswerAsText] decision [Replier.SpeakReaction] applies (voiceless Butler, an
// explicit request, or a #297-d2 long answer). It is the coordinator's pre-render
// seam (#389, ADR-0025/0027): the Ensemble coordinator classifies the reactor BEFORE
// entering the look-ahead pre-render, so a text reactor is routed through the
// post-gate tail — its irreversible TextSink post never happens mid-Lead-playback (an
// audio Reaction is held in the pump lane and discardable; a text post is not). It
// keys on the RAW utterance, matching the modality decision SpeakReaction makes. A
// nil TextSink (every non-Butler) is never text.
func (r *Replier) ReactsAsText(utterance, reaction string) bool {
	if r.cfg.TextSink == nil {
		return false
	}
	voiceless := r.cfg.Persona.Voice.VoiceID == ""
	return AnswerAsText(utterance, reaction, voiceless)
}

// speakDraftModality is the shared body of [Replier.SpeakDraft] and
// [Replier.SpeakReaction]. commitText is what a delivered turn appends as the user
// message (the composite Cross-talk line for a Reaction, the raw utterance for a
// Lead); modalityUtterance is what [AnswerAsText] keys on — always the RAW utterance,
// so a Reaction's quoted Lead line never contaminates the keyword match (#389 finding
// 3). draft is the answer text.
func (r *Replier) speakDraftModality(ctx context.Context, commitText, modalityUtterance, draft string, dispatch func(orchestrator.Reply) error) (delivered string, err error) {
	// Text-modality gate (#389, ADR-0009 2026-07-10 amendment): a Butler with a
	// TextSink routes its winning draft by the SAME [AnswerAsText] decision the routed
	// streaming path uses ([Replier.textModalityTurn]). A text-modality draft — a
	// voiceless Butler (empty VoiceID), an explicit "as text" request, or a long result
	// (#297 d2) — is posted WHOLE to the channel chat with ZERO TTS dispatch, committed
	// user+assistant (ADR-0012 text-delivered commits), and reported with the
	// [orchestrator.ErrTextDelivered] sentinel so the ensemble coordinator publishes
	// TurnEnded(text_delivered), keeping metrics/relay consistent with the routed path.
	// Because this precedes every dispatch, a voiceless Butler can NEVER reach TTS with
	// an empty VoiceID on the ensemble Draft/Speak path OR the Cross-talk Reaction path
	// ([Replier.SpeakReaction] delegates here) — the structural-unreachability guarantee.
	// A failed post delivered nothing, so it commits nothing and surfaces the post error
	// (never the sentinel), mirroring [Replier.textModalityTurn].
	if r.cfg.TextSink != nil && strings.TrimSpace(draft) != "" {
		voiceless := r.cfg.Persona.Voice.VoiceID == ""
		if AnswerAsText(modalityUtterance, draft, voiceless) {
			if err := r.cfg.TextSink(ctx, draft); err != nil {
				return "", err
			}
			r.commitSpoken(r.appendUser(commitText), draft)
			return draft, orchestrator.ErrTextDelivered
		}
	}

	// Deliver-then-commit via the shared emitter (ADR-0012, #444). Lazy
	// user-append (#375, F3): the user message is appended on the FIRST delivered
	// sentence, not eagerly at the top, so a turn that delivers ZERO sentences
	// (every sentence start-errors, or a barge cut it before the first) logs
	// NOTHING — ADR-0012: "If zero sentences were delivered the utterance is not
	// logged at all."
	var split sentenceSplitter
	var turn uint64 // set by the lazy user-append; read only past d.wait()
	d := &deliveredText{
		dispatch:         dispatch,
		voice:            r.cfg.Persona.Voice,
		selfName:         r.cfg.Persona.Name,
		onFirstDelivered: func() { turn = r.appendUser(commitText) },
		pipelined:        orchestrator.PipelinedDispatch(ctx),
	}

	var dispErr error
	for _, sentence := range split.Push(draft) {
		if e := d.emit(sentence); e != nil {
			dispErr = e
			break
		}
	}
	// Flush the trailing unterminated sentence — unless the turn was cut, in which
	// case its tail was never spoken and must not be dispatched.
	if dispErr == nil && ctx.Err() == nil {
		if tail := split.Flush(); tail != "" {
			if e := d.emit(tail); e != nil {
				dispErr = e
			}
		}
	}

	d.wait()
	r.commitSpoken(turn, d.text())

	// A cancel is the expected barge path (the whole ensemble was torn down), not a
	// turn failure; surface only a genuine dispatch error under a live ctx.
	if ctx.Err() != nil {
		return d.text(), nil
	}
	return d.text(), dispErr
}

// deliveredText accumulates one turn's delivered sentences under the
// deliver-then-commit contract (ADR-0012, #444): emit dispatches a sentence and
// appends it to the spoken text ONLY when the orchestrator's turn module
// reports it delivered. A start-error ([orchestrator.SentenceNotDelivered])
// skips the sentence's commit but keeps the turn going — the resulting history
// gap is INTENDED (exactly what the room heard); a cut
// ([orchestrator.SentenceCut], a barge/mute cancel) stops the drain.
// onFirstDelivered, when non-nil, runs once before the first committed sentence
// — the lazy user-message append (#375 F3), so a zero-delivered turn logs
// NOTHING. It is the ONE producer-side emit path (the per-site closures it
// replaced each re-implemented this classification).
type deliveredText struct {
	dispatch         func(orchestrator.Reply) error
	voice            tts.Voice
	onFirstDelivered func()
	spoken           strings.Builder

	// pipelined mirrors [orchestrator.PipelinedDispatch] for this turn (#626).
	// Under a pipelined dispatch the callback returns as soon as the PREVIOUS
	// sentence resolved, so its return value no longer proves delivery: the commit
	// moves onto the [orchestrator.Reply.OnDelivered] hook (fired at ADR-0012's
	// commit point, on the pipeline's goroutine) and [deliveredText.wait] is the
	// barrier every reader of [deliveredText.text] must cross first. Unpipelined,
	// emit commits on the return exactly as before, hook-free.
	pipelined bool
	// mu guards spoken/onFirstDelivered, which the pipelined commit hook writes
	// from a goroutine other than the producer's. wg counts sentences dispatched
	// but not yet resolved.
	mu sync.Mutex
	wg sync.WaitGroup

	// selfName is the Agent's own display name, non-empty only when the Replier
	// knows it. Every sentence this emitter sees has a leading "<selfName>:"
	// speaker label stripped off it before dispatch (2026-07-27): on the streaming
	// path there is no whole-completion string to clean — the model's deltas go
	// splitter → emit → TTS — so this is the one seam that sits upstream of every
	// consumer at once (the TTS dispatch, the ADR-0012 history commit that feeds
	// the next prompt, and the Transcript the Chunker builds from what was
	// dispatched). Every sentence rather than just the first, because a model that
	// adopts the transcript grammar tends to label each LINE of a multi-line reply;
	// the strip needs the Agent's own name plus a colon at a sentence start, which
	// quoted dialogue ("Greta said: …") never produces.
	selfName string

	// attempted: some dispatch reached the synthesizer — audio MAY exist even
	// when nothing committed, because [orchestrator.ErrNotDelivered] covers a
	// #436 mid-stream TTS failure where a fragment already played, not only a
	// start-error. It mirrors turnRun.attempted (set past the pre-dispatch cut
	// check; a SentenceCut here means the turn ctx died, which the fallback's own
	// ctx gate already excludes). The terminal-error fallback keys on it: the
	// canned line must never follow audio the room may already have heard, and
	// d.text() alone under-counts (it holds only COMMITTED sentences).
	attempted bool
}

func (d *deliveredText) emit(sentence string) error {
	// Markdown never reaches the voice (O12). This is the same one-seam argument
	// as the self-prefix strip below: what is scrubbed here is what TTS speaks,
	// what history commits and what the Transcript records. The scrub runs FIRST
	// so a label hidden behind a list or heading marker ("- Mira: Hallo") is at
	// the start of the sentence by the time the strip looks for it.
	sentence = scrubSpoken(sentence)
	if d.selfName != "" {
		sentence = stripSelfPrefix(d.selfName, sentence)
	}
	if !hasSpeakable(sentence) {
		// The whole sentence WAS the label ("Mira:") or markup ("```json"). There
		// is nothing to synthesize, and dispatching it would make TTS reject the
		// request; the turn keeps producing, so the reply arrives intact minus it.
		return nil
	}
	rep := orchestrator.Reply{Sentence: sentence, Voice: d.voice}
	if d.pipelined {
		// Hook-commit (#626): this sentence's outcome is not what dispatch returns,
		// so the commit rides OnDelivered and the wait barrier rides OnResolved.
		d.wg.Add(1)
		rep.OnDelivered = func() { d.commit(sentence) }
		rep.OnResolved = func(orchestrator.SentenceOutcome) { d.wg.Done() }
	}
	err := d.dispatch(rep)
	switch orchestrator.OutcomeOf(err) {
	case orchestrator.SentenceNotDelivered:
		d.attempted = true
		return nil // skip this sentence's commit; the turn is alive, keep producing
	case orchestrator.SentenceCut:
		return err // stop the drain; nothing more is committed
	}
	d.attempted = true
	if !d.pipelined {
		d.commit(sentence)
	}
	return nil
}

// commit appends one DELIVERED sentence to the turn's spoken text, running the
// lazy first-delivery hook before the first of them. Pipelined, it runs on the
// dispatch goroutine — hence the lock.
func (d *deliveredText) commit(sentence string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.onFirstDelivered != nil {
		d.onFirstDelivered()
		d.onFirstDelivered = nil
	}
	if d.spoken.Len() > 0 {
		d.spoken.WriteByte(' ')
	}
	d.spoken.WriteString(sentence)
}

// wait blocks until every sentence this emitter dispatched has resolved, so
// [deliveredText.text] reports the turn's FINAL delivered text. Under an
// unpipelined dispatch nothing is outstanding and it returns at once; pipelined,
// it is the barrier the last sentence's commit lands before — without it the
// history commit races the tail sentence and drops it.
func (d *deliveredText) wait() { d.wg.Wait() }

// text returns the delivered text so far — the history commit. Call
// [deliveredText.wait] first: only then is "so far" the whole turn.
func (d *deliveredText) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.spoken.String()
}

// crossTalkSilence is the sentinel a Cross-talk Reaction emits to DECLINE speaking
// (ADR-0025, #302): the Reaction prompt permits an explicit "stay silent" output to
// avoid hollow "yeah, what he said" filler. A reply equal to it (or empty) means the
// Agent reacts with nothing — no event, no dispatch, no commit (ADR-0012).
const crossTalkSilence = "[SILENCE]"

// crossTalkInstruction is the prompt addendum for a Cross-talk Reaction turn
// (ADR-0025): it frames the just-heard Lead line as cross-talk this Agent may react
// to, and permits the [crossTalkSilence] decline. It rides the volatile Hot Context
// tail ONLY on the React path (ADR-0059 — never the stable system prompt, whose
// cached prefix it would fork), so a normal turn's prompt is unchanged.
const crossTalkInstruction = "Another character has just spoken in the conversation, quoted below. " +
	"React briefly in your own voice — a short agreement, a pointed disagreement, or an aside — " +
	"but only if you have something worth adding. If you would just echo them or have nothing to add, " +
	"reply with exactly " + crossTalkSilence + " and nothing else."

// crossTalkUserText composes the user message for a Cross-talk Reaction (ADR-0025,
// #302): the original utterance, then the Lead's delivered line attributed to it. It
// is the SINGLE composition site — [Replier.React] feeds it to the LLM as the turn's
// user message AND [Replier.SpeakReaction] commits the SAME text to history, so the
// reacting Agent's recorded turn and the prompt it reasoned over never drift.
func crossTalkUserText(userText, leadName, leadText string) string {
	return userText + "\n\n" + leadName + ` says: "` + leadText + `"`
}

// React produces this Agent's would-be Cross-talk Reaction to the Lead's delivered
// line WITHOUT mutating any state — the speculative half of the Reaction phase of an
// Ensemble Turn (ADR-0025, #302). Like [Replier.Draft] it reads Hot Context on a
// history SNAPSHOT (so a reaction that is never spoken commits nothing, ADR-0012),
// but its user message is the composite [crossTalkUserText] and its volatile
// Hot Context tail carries the [crossTalkInstruction] permitting the decline
// sentinel. An empty
// completion OR the [crossTalkSilence] sentinel returns "", nil — the Agent declines
// to react. It honors ctx (bounded by [Config.TurnTimeout]) so a barge tearing the
// whole ensemble down unwinds an in-flight reaction generation.
func (r *Replier) React(ctx context.Context, speakerID, userText, leadName, leadText string) (string, error) {
	return r.reactWithLine(ctx, r.userLine(speakerID, userText), userText, leadName, leadText)
}

// reactWithLine is [Replier.React] with the speaker-attributed user line already
// resolved — the [Cast]'s entry point (#473 review, see [Replier.draftWithLine]):
// the composite the reactor reasons over is built from the line pinned at Draft
// time, so SpeakReaction can commit the identical composite even when the
// SpeakerName cache changes mid-turn. userText stays the RAW utterance for
// memory recall (ADR-0042).
func (r *Replier) reactWithLine(ctx context.Context, userLine, userText, leadName, leadText string) (string, error) {
	ctx, cancel := r.withTurnTimeout(ctx)
	defer cancel()

	// Snapshot + append the composite user message on the COPY (purity, as Draft).
	// The composite's utterance half carries the speaker attribution (the Lead's
	// half is already name-attributed by construction).
	r.mu.Lock()
	history := append(make([]llm.Message, 0, len(r.history)+1), r.history...)
	r.mu.Unlock()
	history = append(history, llm.Message{Role: llm.RoleUser, Text: crossTalkUserText(userLine, leadName, leadText)})
	history = trimHistory(history, r.cfg.HistoryTurns)

	// Recall keys on the raw utterance (the memory the Agent brings to the moment),
	// not the composite — the cross-talk line is context, not a new topic. The
	// directive consult only PEEKS (a reaction that stays silent must not burn a
	// turn of the budget, ADR-0059).
	in := r.gather(ctx, userText, false)

	// The crossTalkInstruction rides the volatile tail, NOT the stable system
	// prompt (ADR-0059): appended there it would fork the cached prefix right
	// after the Persona, invalidating the whole history for the provider's
	// prefix cache. In the tail it sits with the other per-turn content —
	// before the directive, which keeps the strongest recency slot.
	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt()})
	msgs = append(msgs, history...)
	msgs = appendVolatileTail(msgs, factsBlock(in.facts), locationBlock(in.location), memoryBlock(in.mem), crossTalkInstruction, directiveBlock(in.directive))

	reply, err := r.engine.Generate(ctx, msgs)
	if err != nil {
		return "", err
	}
	reply = stripSelfPrefix(r.cfg.Persona.Name, strings.TrimSpace(reply))
	if isSilenceSentinel(reply) {
		return "", nil // the Reactor declined (ADR-0025 zero-delivered)
	}
	return reply, nil
}

// isSilenceSentinel reports whether reply is a Cross-talk decline: empty, or nothing
// but the [crossTalkSilence] token (#302 hardening). Live models emit case/punctuation
// variants — "[silence]", "[SILENCE].", `"[silence]."` — so the match is
// case-insensitive over the reply stripped of surrounding whitespace, quotes, and
// trailing punctuation. A reply that merely MENTIONS the token amid other words is a
// real reply, not a decline (a contains-ONLY check, not a substring check).
func isSilenceSentinel(reply string) bool {
	trimmed := strings.Trim(strings.TrimSpace(reply), " \t\n\"'.,!?")
	return trimmed == "" || strings.EqualFold(trimmed, crossTalkSilence)
}

// SpeakReaction speaks an already-generated Cross-talk Reaction (from a prior
// [Replier.React]) as this Agent's own sub-turn (ADR-0025, #302). It commits the SAME
// composite user text React reasoned over ([crossTalkUserText]) so the committed user
// message and the prompt never drift, and delivers the reaction under
// deliver-then-commit (ADR-0012). The text-modality decision (#389), however, keys on
// the RAW utterance — NOT the composite — so the Lead's quoted line can never flip a
// voiced reactor to text via a keyword match ("Post it on the tavern board.", finding
// 3). It returns the delivered text; a ctx-cancel (a barge cutting the reaction
// mid-playback) commits only the sentences spoken before the cut.
func (r *Replier) SpeakReaction(ctx context.Context, speakerID, userText, leadName, leadText, reaction string, dispatch func(orchestrator.Reply) error) (delivered string, err error) {
	return r.speakDraftModality(ctx, crossTalkUserText(r.userLine(speakerID, userText), leadName, leadText), userText, reaction, dispatch)
}

// ReplyStream returns the [orchestrator.StreamReplyFunc] that drives this loop
// in streaming mode (B1): it dispatches each sentence of the reply to TTS the
// moment it is ready, so first audio begins after the first sentence rather than
// the whole completion. Install it as [orchestrator.ReplyStrategy.Stream].
//
// It requires the configured [Engine] to implement [StreamingEngine]; if it does
// not, every turn falls back to a single post-completion dispatch (the behaviour
// of [Replier.Reply]) so the wiring is always safe, just not incremental.
func (r *Replier) ReplyStream() orchestrator.StreamReplyFunc {
	return func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		if e.Target.AgentID != r.cfg.Persona.AgentID {
			return nil // not addressed to this Agent
		}
		// Bound the streaming turn's GENERATION with the same per-turn deadline as
		// the batch path: production wires this path, and without it a
		// thinking-then-stalling provider completion runs unbounded (the Gemini
		// client has no overall HTTP timeout by design), so a wedged turn would
		// never produce first audio. The PRE-timeout parent is what playback runs
		// under and the fallback gates' silence judge (#473 review, see
		// [Replier.Reply]): the deadline must not cut a reply that is merely long
		// to say.
		parent := ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := r.withTurnTimeout(parent)
		defer cancel()
		return r.streamTurn(ctx, parent, e.SpeakerID, e.Text, dispatch)
	}
}

// streamTurn runs one streaming Agent turn: it assembles Hot Context, drives the
// [StreamingEngine], segments the streamed text into sentences, and hands each
// to dispatch.
//
// Generation and playback are DECOUPLED. A generation goroutine drains the
// engine under ctx (the [Config.TurnTimeout] child) as fast as the provider
// produces text, pushing each finished sentence onto a bounded queue; the
// calling goroutine is the dispatcher, delivering the queue to dispatch one
// sentence at a time, at speaking pace, under the caller's turn ctx. Consuming
// the provider stream in lockstep with playback — the earlier shape — put the
// whole reply's PLAYBACK under the generation deadline, so any answer longer
// than a minute of audio was cut off mid-sentence and reported as a provider
// error, and a barged turn never read the provider's trailing usage chunk.
//
// What still holds, exactly as before:
//
//   - Cancellation. parent is the caller's turn ctx; a barge, mute or supersede
//     cancels it, and ctx with it. The dispatcher stops at the first sentence
//     dispatch reports cut and cancels generation; the generation goroutine is
//     joined before the turn returns, so nothing outlives it.
//   - Deliver-then-commit (ADR-0012). Only sentences dispatch reported delivered
//     are committed. A sentence still in the queue when the turn is cut was
//     generated, never spoken, and is dropped.
//
// What changed for Tools (ADR-0030, amendment of 2026-11-20): the engine's
// rounds are no longer paced by the room. Lockstep meant a tool round after
// spoken prose only started once that prose had been delivered, so a barge
// during it prevented the round; now every LLM and tool round runs to completion
// as fast as the provider allows, even if the players cut the reply after its
// first sentence. That is safe for what exists — read-only Tools, and
// remember_knowledge, whose only effect is a GM-reviewed proposal (ADR-0052) —
// and it is exactly why a future Tool with a real side effect MUST use
// turn-commit and must never execute inline.
//
// A generation that fails or times out mid-reply still has its already-queued
// sentences spoken (they are what the model said before the failure); only the
// unterminated tail is dropped. A reply that reaches [MaxSpokenRunes] is stopped
// the same way, but that is a limit rather than a failure: it is reported via
// OnError and the turn ends cleanly. parent's liveness, not ctx's, decides barge
// silence: the turn's own TurnTimeout expiry reports OnError and — when nothing
// was attempted — speaks the fallback (#473 review).
func (r *Replier) streamTurn(ctx, parent context.Context, speakerID, text string, dispatch func(orchestrator.Reply) error) error {
	turn := r.appendUser(r.userLine(speakerID, text))

	// The recallers run between the lock sections (see [Replier.turn]): outside
	// r.mu, under ctx, each degrading to nothing rather than stalling the
	// streaming turn. Recall keys on the RAW utterance, never the attributed line
	// (ADR-0042). The directive consult CONSUMES a turn of its budget — this is
	// the committed reply path production wires (ADR-0059).
	in := r.gather(ctx, text, true)

	r.mu.Lock()
	messages := r.hotContextLocked(in)
	r.mu.Unlock()

	// TextSink installed (the Butler, #299): decide modality on the WHOLE answer, so
	// this path needs a batch completion rather than incremental streaming. A
	// text-modality answer posts via the sink with zero TTS dispatch; a spoken one
	// is sentence-split and dispatched. See [Replier.textModalityTurn].
	if r.cfg.TextSink != nil {
		return r.textModalityTurn(ctx, parent, turn, text, messages, dispatch)
	}

	streamer, ok := r.engine.(StreamingEngine)
	if !ok {
		// Non-streaming engine: fall back to one completion, then dispatch it whole.
		return r.fallbackTurn(ctx, parent, turn, messages, dispatch)
	}

	// Deliver-then-commit via the shared emitter (ADR-0012, #444): a sentence
	// joins the history commit only once the turn module reports it delivered —
	// fully synthesized under a live turn ctx. A dispatch rejected because the
	// turn was cancelled (mute/barge) never reaches the spoken text: the room
	// never heard it.
	d := &deliveredText{dispatch: dispatch, voice: r.cfg.Persona.Voice, selfName: r.cfg.Persona.Name,
		pipelined: orchestrator.PipelinedDispatch(ctx)}

	// Generation: drain the engine into the queue. The queue never blocks the
	// generator (that would put playback back under the generation deadline); it
	// is bounded by the spoken-text cap instead.
	genCtx, stopGen := context.WithCancel(ctx)
	defer stopGen()
	queue := make(chan string, sentenceQueueCap)
	genDone := make(chan struct{})
	var genErr error // written by the generation goroutine; read after <-genDone
	go func() {
		defer close(genDone)
		defer close(queue)
		// Pushing never blocks: at the cap generation is stopped instead of
		// stalled against playback.
		queued := 0 // runes pushed so far, guarded by the feed's lock
		feed := newSentenceFeed(func(sentence string) error {
			queued += utf8.RuneCountInString(sentence) + 1
			if queued > MaxSpokenRunes {
				return errReplyTooLong
			}
			select {
			case queue <- sentence:
				return nil
			default:
				return errReplyTooLong // unreachable while sentenceQueueCap holds
			}
		}, r.cfg.SentenceIdleFlush)
		onText := func(delta string) error {
			// A cancelled turn stops an engine that does not watch ctx itself at
			// its next delta, as the dispatch error used to.
			if err := genCtx.Err(); err != nil {
				return err
			}
			return feed.Write(delta)
		}
		_, err := streamer.GenerateStream(genCtx, messages, onText)
		// Flush the trailing sentence of a COMPLETE generation. A failed or
		// cancelled one ended mid-sentence: its tail must not be spoken. Close
		// also retires the feed's idle timer BEFORE the queue is closed below.
		if cerr := feed.Close(err == nil); err == nil {
			err = cerr
		}
		genErr = err
	}()

	// Dispatch: deliver the queue in order. emit returns an error only for a CUT
	// (barge/mute/supersede, or a dispatch that aborts the turn) — then nothing
	// more is delivered and generation is cancelled.
	var cutErr error
	for sentence := range queue {
		if err := d.emit(sentence); err != nil {
			cutErr = err
			break
		}
	}
	stopGen()

	// Commit as soon as delivery is settled, BEFORE joining the generation
	// goroutine: a superseded turn's commit should land at once, not after its
	// provider call unwinds.
	d.wait()
	r.commitSpoken(turn, d.text())
	<-genDone

	turnErr := cutErr
	if turnErr == nil {
		turnErr = genErr
	}
	// The spoken-text cap is a limit, not a turn failure: say so, and end the
	// turn on what was spoken.
	if errors.Is(turnErr, errReplyTooLong) {
		if r.cfg.OnError != nil && parent.Err() == nil {
			r.cfg.OnError(turnErr)
		}
		return nil
	}

	// A cancellation of the CALLER's turn ctx is the expected barge-in path, not
	// a turn failure: report only a genuine engine error — which includes the
	// turn's OWN TurnTimeout expiry (the parent stays live; a hung provider is a
	// terminal failure, not a barge — #473 review).
	if turnErr != nil && parent.Err() == nil {
		if r.cfg.OnError != nil {
			r.cfg.OnError(turnErr)
		}
		// Terminal engine error before ANY dispatch was ATTEMPTED: speak the
		// canned fallback and report the turn delivered (nil) rather than
		// abandoned. Once a dispatch was attempted the room MAY have heard audio
		// — a delivered sentence, or a #436 mid-stream TTS failure's fragment
		// (ErrNotDelivered, which commits nothing) — so no canned line is
		// appended; gating on the committed text alone would speak after a
		// partial reply (#473 review).
		if !d.attempted && r.speakFallback(parent, dispatch) {
			return nil
		}
		return turnErr
	}
	return nil
}

// fallbackTurn handles a streaming reply when the engine cannot stream: it runs
// one completion and dispatches the whole reply as a single sentence, mirroring
// the batch [Replier.turn] so a non-streaming engine still speaks. parent is
// the caller's PRE-timeout turn ctx, the fallback's silence gate (#473 review).
func (r *Replier) fallbackTurn(ctx, parent context.Context, turn uint64, messages []llm.Message, dispatch func(orchestrator.Reply) error) error {
	reply, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if parent.Err() == nil {
			if r.cfg.OnError != nil {
				r.cfg.OnError(err)
			}
			// Terminal engine error — including the turn's own TurnTimeout expiry
			// (the parent stays live): speak the canned fallback and report the
			// turn delivered (nil) instead of dead air. A barge cancel (or a
			// voiceless Persona) propagates the error as before.
			if r.speakFallback(parent, dispatch) {
				return nil
			}
		}
		return err
	}
	reply = stripSelfPrefix(r.cfg.Persona.Name, strings.TrimSpace(reply))
	if reply == "" {
		return nil
	}
	// Deliver-then-commit (ADR-0012) through the shared emitter (#626): the commit
	// hangs off the delivery hook and the wait barrier, because under a pipelined
	// dispatch this single dispatch returns before the reply has resolved — its
	// return no longer proves the room heard anything. A start-error commits
	// nothing and is swallowed (returning it would misclassify the turn as
	// provider_error; the reactor already recorded tts_error); a cut stops the
	// turn.
	d := &deliveredText{dispatch: dispatch, voice: r.cfg.Persona.Voice, selfName: r.cfg.Persona.Name,
		pipelined: orchestrator.PipelinedDispatch(ctx)}
	err = d.emit(reply)
	d.wait()
	r.commitSpoken(turn, d.text())
	if orchestrator.OutcomeOf(err) == orchestrator.SentenceCut {
		return err
	}
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
//
// The text branch returns the [orchestrator.ErrTextDelivered] sentinel (#299) so
// the reactor publishes a TurnEnded(text_delivered) terminal event: a text turn
// dispatches no TTS and reaches no first audio, so without this signal the
// metrics TTL sweep would miscount a delivered answer as abandoned. The spoken
// branch stays silent on success (first audio is its terminal signal). parent
// is the caller's PRE-timeout turn ctx, the fallback's silence gate (#473
// review).
func (r *Replier) textModalityTurn(ctx, parent context.Context, turn uint64, utterance string, messages []llm.Message, dispatch func(orchestrator.Reply) error) error {
	answer, err := r.engine.Generate(ctx, messages)
	if err != nil {
		if parent.Err() == nil {
			if r.cfg.OnError != nil {
				r.cfg.OnError(err)
			}
			// Terminal engine error — including the turn's own TurnTimeout expiry
			// (the parent stays live): a VOICED Butler speaks the canned fallback
			// (delivered, nil); a voiceless one has no Voice to speak it with —
			// speakFallback refuses (empty VoiceID never reaches TTS) and the
			// error propagates as before.
			if r.speakFallback(parent, dispatch) {
				return nil
			}
		}
		return err
	}
	answer = stripSelfPrefix(r.cfg.Persona.Name, strings.TrimSpace(answer))
	if answer == "" {
		return nil
	}

	voiceless := r.cfg.Persona.Voice.VoiceID == ""
	if AnswerAsText(utterance, answer, voiceless) {
		// Text delivery: post the whole answer to the channel chat, then commit it.
		if err := r.cfg.TextSink(ctx, answer); err != nil {
			return err
		}
		r.commitSpoken(turn, answer)
		// Signal a text-delivered terminal outcome (#299): this turn dispatched no
		// TTS, so it reaches no first audio. Returning the sentinel lets the reactor
		// publish TurnEnded(text_delivered) — a SUCCESS — instead of the metrics TTL
		// sweep miscounting it as an abandoned/no_first_audio turn.
		return orchestrator.ErrTextDelivered
	}

	// Spoken delivery: sentence-split the whole answer and dispatch each through
	// the shared emitter (ADR-0012, #444), committing only what was delivered.
	var split sentenceSplitter
	d := &deliveredText{dispatch: dispatch, voice: r.cfg.Persona.Voice, selfName: r.cfg.Persona.Name,
		pipelined: orchestrator.PipelinedDispatch(ctx)}
	for _, sentence := range split.Push(answer) {
		if err := d.emit(sentence); err != nil {
			d.wait()
			r.commitSpoken(turn, d.text())
			return err
		}
	}
	if tail := split.Flush(); tail != "" {
		if err := d.emit(tail); err != nil {
			d.wait()
			r.commitSpoken(turn, d.text())
			return err
		}
	}
	d.wait()
	r.commitSpoken(turn, d.text())
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

// appendUser appends the utterance as a user message under the history lock,
// trims to [Config.HistoryTurns], and returns the id of the turn it opens — the
// anchor [Replier.commitSpoken] places that turn's assistant reply behind. It is
// the single user-append site shared by the streaming/batch turns' eager append,
// [Replier.SpeakDraft]'s deliver-then-commit lazy append, and its text-modality
// branch (#389).
func (r *Replier) appendUser(text string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTurn++
	r.history = append(r.history, llm.Message{Role: llm.RoleUser, Text: text})
	r.turnOf = append(r.turnOf, r.lastTurn)
	r.trimHistoryLocked()
	return r.lastTurn
}

// commitSpoken records the text actually delivered to the pump as the assistant
// message of turn (ADR-0012). An empty string — a turn cancelled before any
// sentence was spoken — records nothing, so a barged-out turn leaves no phantom
// assistant message.
//
// The message is placed directly behind its OWN turn's user message, not at the
// end of the history. The two differ only when a superseded turn commits its
// delivered prefix after the superseding turn already recorded its user line
// (barge-in runs each turn on its own goroutine): appended, the history would
// read user A, user B, assistant A — and if turn B assembled its prompt in that
// window, the model was handed a conversation ENDING on the half-finished answer
// to the previous question. A turn whose user message is gone (trimmed away
// while it was still speaking) commits nothing: its place in the conversation
// no longer exists, and appending the reply at the end would file it under a
// question it does not answer.
func (r *Replier) commitSpoken(turn uint64, spoken string) {
	spoken = strings.TrimSpace(spoken)
	if spoken == "" {
		return
	}
	msg := llm.Message{Role: llm.RoleAssistant, Text: spoken}
	r.mu.Lock()
	defer r.mu.Unlock()
	at := -1
	for i := len(r.turnOf) - 1; i >= 0; i-- {
		if r.turnOf[i] == turn {
			at = i + 1
			break
		}
	}
	if at < 0 {
		return
	}
	r.history = slices.Insert(r.history, at, msg)
	r.turnOf = slices.Insert(r.turnOf, at, turn)
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

// directive consults the configured [DirectiveRecaller] for this turn's GM
// directive, keyed by the Agent's id. consume marks a committed reply path
// (see [DirectiveRecaller.Directive]). A nil recaller (the unconfigured
// default) returns "", so the slot stays empty and the prompt is byte-identical
// to the pre-directive path (ADR-0059). The recaller never errors and respects
// ctx.
func (r *Replier) directive(ctx context.Context, consume bool) string {
	if r.cfg.Directive == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Directive.Directive(ctx, r.cfg.Persona.AgentID, consume))
}

// location resolves this turn's party-location clause (#540). Like the other tail
// slots it degrades silently to "" — a spatial read is never worth stalling or
// failing a turn over.
func (r *Replier) location(ctx context.Context) string {
	if r.cfg.Location == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Location.Location(ctx, r.cfg.Persona.AgentID))
}

// hotInputs is one turn's volatile Hot Context: what the four recallers
// returned, each zero when its recaller is unconfigured or degraded.
type hotInputs struct {
	mem       Memory
	facts     []string
	directive string
	location  string
}

// gather consults the four Hot Context recallers — memory, KG facts, GM
// directive, party location — CONCURRENTLY and returns what each produced. They
// are independent reads (an embedding search, three DB lookups), each bounded by
// its own budget and each degrading to nothing on its own, so running them one
// after another only added their latencies together on the path to first audio.
// The order the results are RENDERED in is fixed by [appendVolatileTail]'s
// callers and does not depend on which recaller answers first.
//
// text is the RAW utterance memory recall keys on; consume marks a committed
// reply path for the directive's turn budget (see [DirectiveRecaller]). A turn
// with at most one recaller configured runs it inline, with no goroutine.
func (r *Replier) gather(ctx context.Context, text string, consume bool) hotInputs {
	var in hotInputs
	var jobs []func()
	if r.cfg.Memory != nil {
		jobs = append(jobs, func() { in.mem = r.recall(ctx, text) })
	}
	if r.cfg.Facts != nil {
		jobs = append(jobs, func() { in.facts = r.facts(ctx) })
	}
	if r.cfg.Directive != nil {
		jobs = append(jobs, func() { in.directive = r.directive(ctx, consume) })
	}
	if r.cfg.Location != nil {
		jobs = append(jobs, func() { in.location = r.location(ctx) })
	}
	if len(jobs) == 0 {
		return in
	}
	// Each job writes its own field of in; the WaitGroup is the only
	// synchronization they need. The last one runs on the calling goroutine.
	var wg sync.WaitGroup
	for _, job := range jobs[:len(jobs)-1] {
		wg.Go(job)
	}
	jobs[len(jobs)-1]()
	wg.Wait()
	return in
}

// hotContextLocked assembles the Hot Context message list for one call: the
// STABLE system prompt (Persona + roster + spoken-output and audio-markup
// rules), the recent Transcript (the bounded history), and — LAST — the
// volatile tail message carrying this turn's KG facts, party location, recalled
// memory, and GM directive (each zero when unconfigured or degraded; an
// all-empty tail appends no message). Caller holds r.mu.
//
// The stable-prefix / volatile-tail split is deliberate (ADR-0059): the default
// provider's prefix cache (Groq, automatic — no cache_control API) matches
// request tokens from the start and stops at the first difference. With facts
// and memory inside the system prompt (the pre-0059 order) every turn's fresh
// facts forked the prefix right after the Persona, so the roster, markup, and
// the ENTIRE history missed the cache each turn. With the volatile content
// trailing the append-only history, the whole conversation up to the previous
// turn stays cache-hittable — lower TTFT, half-priced cached tokens.
func (r *Replier) hotContextLocked(in hotInputs) []llm.Message {
	msgs := make([]llm.Message, 0, len(r.history)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt()})
	msgs = append(msgs, r.history...)
	return appendVolatileTail(msgs, factsBlock(in.facts), locationBlock(in.location), memoryBlock(in.mem), directiveBlock(in.directive))
}

// appendVolatileTail appends the volatile Hot Context tail — the given blocks
// joined by blank lines — as ONE trailing system-role message (ADR-0059). All
// blocks empty appends nothing, so a turn with no facts, no memory, and no
// directive stays byte-identical to the plain system+history prompt. A trailing
// system message is safe across every provider: the OpenAI-compatible adapters
// (Groq, Gemini) keep it positionally last, and the anthropic adapter folds it
// into the system field — semantically the pre-0059 placement.
func appendVolatileTail(msgs []llm.Message, blocks ...string) []llm.Message {
	var b strings.Builder
	for _, block := range blocks {
		if block == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
	}
	if b.Len() == 0 {
		return msgs
	}
	return append(msgs, llm.Message{Role: llm.RoleSystem, Text: b.String()})
}

// systemPrompt builds the STABLE system prompt — the per-session-constant prefix
// of every request (ADR-0059) — in slot order: the Persona, the
// speaker-attribution section (the table roster — see [Config.PlayerCharacters];
// rendered ONLY when a SpeakerName resolver is wired, since it describes the
// "Name: text" prefix that resolver produces), the [spokenOutputRule] (for an
// Agent whose every reply is spoken), and the Voice's provider-specific
// audio-markup instruction from [tts.Synthesizer.AudioMarkupPrompt] (required by
// ADR-0022). Everything here is fixed at session start; the per-turn content
// (KG facts, party location, recalled memory, GM directive) rides the volatile
// tail message instead ([appendVolatileTail]) so the provider's prefix cache
// survives across turns. An unset roster omits its block entirely.
func (r *Replier) systemPrompt() string {
	var b strings.Builder
	if p := strings.TrimSpace(r.cfg.Persona.Markdown); p != "" {
		b.WriteString(p)
	}
	if r.cfg.SpeakerName != nil {
		if block := speakerRosterBlock(r.cfg.PlayerCharacters, r.cfg.FellowNPCs); block != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(block)
		}
		// The output-format rule rides the SAME gate as the prefix convention
		// itself — a wired SpeakerName is exactly when user lines carry a "Name: "
		// label — but NOT the roster block's gate: that block is empty when no
		// player characters and no fellow NPCs are configured, while the lines are
		// still labelled "Player / DM: ". Shown a grammar, models imitate it; this
		// is the sentence that tells the Agent the grammar is inbound-only.
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(selfPrefixRule)
	}
	markup := r.cfg.Synthesizer.AudioMarkupPrompt(r.cfg.Persona.Voice)
	// Spoken-output rule (O12): only for an Agent that never answers in text —
	// voiced, and without a TextSink (see [spokenOutputRule]).
	if r.cfg.Persona.Voice.VoiceID != "" && r.cfg.TextSink == nil {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(spokenOutputRule)
		if markup != "" {
			b.WriteString(spokenOutputMarkupExemption)
		}
	}
	if markup != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(markup)
	}
	return b.String()
}

// selfPrefixRule is the output-format half of the speaker-prefix convention: the
// prompt teaches the Agent to READ "Name: text" labels, and this tells it never
// to WRITE one. It is session-constant, so it belongs in the stable system
// prompt rather than the volatile tail (ADR-0059's cache-stable prefix).
//
// It is deliberately phrased as "how you answer", not "everything you write is
// spoken": a Butler with a TextSink may answer in channel text (#299), where the
// label is just as wrong but the reason is not that it would be read aloud. The
// deterministic [stripSelfPrefix] backstop stays the guarantee — this only makes
// the mistake rarer, and a prompt alone can never make it impossible.
const selfPrefixRule = "Those name labels mark who is speaking TO you. They are never part of how you answer: " +
	"write only the words you say, with no name and no colon in front of them."

// factsBlock renders the KG-facts slot of the volatile tail as a flat-text
// prompt section: a fixed header followed by the already-rendered fact strings
// joined by blank lines. The agent is a dumb joiner — the fact strings arrive
// fully formatted from the FactsRecaller (#126). No facts yields "" so the
// block is dropped entirely (the byte-identical guarantee).
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

// MaxLocationChars bounds the location clause hard. The whole justification for
// putting location in the prompt at all is that it is ONE short clause; without a
// bound it would become another uncapped block competing with the fact budget,
// which is the thing #539 exists to avoid.
const MaxLocationChars = 160

// locationBlock renders the party-location slot of the volatile tail (#540,
// ADR-0059 note). It is ONE clause — "You are in the Rusty Anchor, in Saltmarsh's
// harbour district" — rune-capped and replaced per turn.
//
// It rides the VOLATILE tail, never the stable prefix: the party moves, and a
// per-turn-changing line in the cache-stable system prompt would break prefix
// caching on every move — the exact cost ADR-0059 was written to avoid. An empty
// location yields "" so the block is dropped entirely (the byte-identical
// guarantee).
func locationBlock(location string) string {
	if location == "" {
		return ""
	}
	r := []rune(location)
	if len(r) > MaxLocationChars {
		location = string(r[:MaxLocationChars]) + "…"
	}
	return "## Where you are right now\n\n" + location
}

// directiveBlock renders the GM-directive slot of the volatile tail (ADR-0059):
// the private steering note plus the secrecy contract — the direction shapes the
// Agent's next replies but must never surface to the table, in speech or in
// paraphrase. An empty directive yields "" so the block is dropped entirely
// (the byte-identical guarantee).
func directiveBlock(directive string) string {
	if directive == "" {
		return ""
	}
	return "## Private direction from your GM\n\n" + directive + "\n\n" +
		"Let this direction shape your next replies while staying fully in character. " +
		"It is a secret stage note: never quote it, mention it, or hint that it exists — " +
		"the players must not be able to tell you were directed."
}

// speakerRosterBlock renders the speaker-attribution section: the "Name: text"
// user-line convention (the prefix — not persona or memories — identifies the
// human addressing the NPC), the player characters as the humans at the table,
// the fellow NPCs by name (so an NPC named like a human's display name cannot be
// misread as the speaker), and the generic [unknownSpeakerLabel]. Kept to a few
// lines — this rides every voice turn's prompt. An empty roster (no players AND
// no fellow NPCs) yields "" so the block is dropped entirely (the byte-identical
// guarantee); the caller additionally gates on a wired SpeakerName resolver,
// without which no prefix exists for the section to describe.
func speakerRosterBlock(players, npcs []string) string {
	if len(players) == 0 && len(npcs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Who is speaking\n\n")
	b.WriteString(`Each user line begins with the name of the HUMAN speaking it, as "Name: text". This prefix — not your persona, your memories, or past conversation — tells you who is addressing you.`)
	if len(players) > 0 {
		b.WriteString("\nPlayer characters at the table (each played by a human): ")
		b.WriteString(strings.Join(players, ", "))
		b.WriteString(".")
	}
	if len(npcs) > 0 {
		b.WriteString("\nFellow NPCs (AI-played like you; a user-line prefix never refers to them): ")
		b.WriteString(strings.Join(npcs, ", "))
		b.WriteString(".")
	}
	b.WriteString("\nLines prefixed \"" + unknownSpeakerLabel + "\" come from an unidentified human.")
	return b.String()
}

// trimHistoryLocked applies [trimHistory] to the live history and its turn
// index. Caller holds r.mu.
func (r *Replier) trimHistoryLocked() {
	cut := trimCut(r.history, r.cfg.HistoryTurns)
	if cut == 0 {
		return
	}
	r.history = append([]llm.Message(nil), r.history[cut:]...)
	r.turnOf = append([]uint64(nil), r.turnOf[cut:]...)
}

// trimHistory bounds history to roughly keep user/assistant messages, trimming
// in BLOCKS with hysteresis: nothing is dropped until the history has grown past
// twice keep, and then it is cut back to the most recent keep in one step. The
// trimmed window is moved forward to begin on a user message.
//
// Both properties are there for the provider. A window that slides one message
// per append starts on a different message every turn — from the ninth exchange
// on, with keep 16 — so the request's cacheable prefix (ADR-0059) ends right
// after the system prompt and the whole history is re-billed each turn; trimmed
// in blocks, the prefix is stable for keep/2 exchanges at a time. And a window
// that opens on an assistant message (or, were tool messages ever kept here, on
// a tool result cut off from its call) is a conversation no provider is obliged
// to accept: starting on a user message is the one cut that can never split a
// reply from its question or a tool result from its call.
//
// It re-slices onto a fresh backing array when it trims (so the dropped turns
// can be GC'd), and returns history unchanged (same backing) when it does not. A
// zero or negative keep means unbounded.
func trimHistory(history []llm.Message, keep int) []llm.Message {
	cut := trimCut(history, keep)
	if cut == 0 {
		return history
	}
	return append([]llm.Message(nil), history[cut:]...)
}

// trimCut returns how many leading messages [trimHistory] drops: 0 while the
// history is within the hysteresis band, else the index of the first user
// message at or after len-keep. A window holding no user message at all keeps
// the plain len-keep cut.
func trimCut(history []llm.Message, keep int) int {
	if keep <= 0 || len(history) <= 2*keep {
		return 0
	}
	cut := len(history) - keep
	for i := cut; i < len(history); i++ {
		if history[i].Role == llm.RoleUser {
			return i
		}
	}
	return cut
}
