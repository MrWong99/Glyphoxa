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
	"strings"
	"sync"

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

// Config configures a [Replier].
type Config struct {
	// Persona is the Agent this loop voices.
	Persona Persona

	// Provider is the LLM the loop calls. Required.
	Provider llm.Provider

	// Synthesizer supplies the audio-markup instruction for the system prompt
	// via [tts.Synthesizer.AudioMarkupPrompt]. Required: ADR-0022 makes the
	// Persona/LLM layer responsible for emitting text in the Voice's provider
	// format, and that instruction is sourced here.
	Synthesizer tts.Synthesizer

	// Model overrides the Provider's default model for this Agent. Optional.
	Model string

	// MaxTokens caps each completion. Zero lets the Provider choose. Optional.
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
}

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
	cfg Config

	mu      sync.Mutex
	history []llm.Message // user/assistant turns only; system prompt is rebuilt each call
}

// NewReplier constructs a [Replier]. cfg.Provider and cfg.Synthesizer must be
// non-nil; passing nil for either panics, mirroring the orchestrator stages'
// fail-fast constructors.
func NewReplier(cfg Config) *Replier {
	if cfg.Provider == nil {
		panic("agent.NewReplier: Provider must not be nil")
	}
	if cfg.Synthesizer == nil {
		panic("agent.NewReplier: Synthesizer must not be nil")
	}
	return &Replier{cfg: cfg}
}

// Reply returns the [orchestrator.ReplyFunc] that drives this loop. Install it
// with [orchestrator.WithReply]. The returned closure runs synchronously inside
// the reply reactor's bus callback (ADR-0026): it assembles Hot Context, makes
// one blocking LLM call, and returns the spoken reply. On a route for a
// different Agent it returns nil (says nothing); on an LLM failure it returns
// nil and reports via [Config.OnError].
func (r *Replier) Reply() orchestrator.ReplyFunc {
	return func(e voiceevent.AddressRouted) []orchestrator.Reply {
		if e.Target.AgentID != r.cfg.Persona.AgentID {
			return nil // not addressed to this Agent
		}
		return r.turn(context.Background(), e.Text)
	}
}

// turn runs one Agent turn for the given utterance text: it appends the user
// message, assembles the [llm.Request] (system prompt + bounded history), calls
// the Provider, accumulates the streamed text, records the assistant reply, and
// returns it as a single [orchestrator.Reply] in the Agent's Voice. An empty
// completion or a Provider error yields no reply (the error is reported via
// OnError).
func (r *Replier) turn(ctx context.Context, text string) []orchestrator.Reply {
	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleUser, Text: text})
	r.trimHistoryLocked()
	req := llm.Request{
		Model:     r.cfg.Model,
		MaxTokens: r.cfg.MaxTokens,
		Messages:  r.hotContextLocked(),
	}
	r.mu.Unlock()

	reply, err := r.complete(ctx, req)
	if err != nil {
		if r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		return nil
	}
	if reply == "" {
		return nil
	}

	r.mu.Lock()
	r.history = append(r.history, llm.Message{Role: llm.RoleAssistant, Text: reply})
	r.mu.Unlock()

	return []orchestrator.Reply{{Sentence: reply, Voice: r.cfg.Persona.Voice}}
}

// complete drives one Provider stream to completion, concatenating the text
// deltas into the spoken reply. Tool-call events are ignored in v1.0 — the
// Agent loop requests no tools, so the model emits none; when the tool-use loop
// (ADR-0028) takes over it will own this drain and act on [llm.EventToolCall].
func (r *Replier) complete(ctx context.Context, req llm.Request) (string, error) {
	stream, err := r.cfg.Provider.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for ev := range stream {
		if ev.Type == llm.EventText {
			b.WriteString(ev.Text)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// hotContextLocked assembles the Hot Context message list for one call: the
// system prompt (Persona + KG-facts placeholder + audio-markup instruction)
// followed by the recent Transcript (the bounded history). Caller holds r.mu.
func (r *Replier) hotContextLocked() []llm.Message {
	msgs := make([]llm.Message, 0, len(r.history)+1)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Text: r.systemPrompt()})
	msgs = append(msgs, r.history...)
	return msgs
}

// systemPrompt builds the system prompt from the three Hot Context inputs:
// the Persona, a KG-facts placeholder (the KG layer lands later — ADR-0008),
// and the Voice's provider-specific audio-markup instruction from
// [tts.Synthesizer.AudioMarkupPrompt] (required by ADR-0022).
func (r *Replier) systemPrompt() string {
	var b strings.Builder
	if p := strings.TrimSpace(r.cfg.Persona.Markdown); p != "" {
		b.WriteString(p)
	}
	// KG facts placeholder: the per-Campaign knowledge graph is wired in later
	// (ADR-0008). Hot Context reserves the slot now so the prompt shape is
	// stable when facts arrive.
	if markup := r.cfg.Synthesizer.AudioMarkupPrompt(r.cfg.Persona.Voice); markup != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(markup)
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
