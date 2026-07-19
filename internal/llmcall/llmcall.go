// Package llmcall is the shared drain-and-meter LLM completion caller for
// off-session (web-tier) text generation: the recap engine (#272) and the
// campaign-assist engine (#479) drive single system+user completions through
// one implementation, so the subtle ADR-0045 metering semantics — reported
// usage verbatim, ceil(chars/4) estimates only on COMPLETED calls, reported-only
// metering on error/truncation — cannot drift between call sites.
package llmcall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// Caller drives one feature's LLM completions against a fixed provider/model,
// metering each via Rec and accumulating the token totals for the caller's
// attribution log.
//
// Model is the request model (empty lets the adapter pick its default).
// PriceModel is the model the spend sink prices on: it equals Model except on
// the default path, where Model is "" but PriceModel is the adapter's default
// model, so (provider, "") is never a price-map miss (#272 review). The request
// model stays "" so the cassette hash and the adapter default are unchanged.
// ErrPrefix tags returned errors with the owning feature ("recap", "assist").
type Caller struct {
	Ctx        context.Context
	Provider   llm.Provider
	Model      string
	PriceModel string
	Label      observe.Provider
	Rec        observe.StageRecorder
	ErrPrefix  string

	TotalIn  int
	TotalOut int
}

// Call runs one completion (system + user), drains it to close, and returns the
// accumulated text. Usage is metered from the provider-reported [llm.EventUsage]
// or, when none arrives on a COMPLETED call, a documented ceil(chars/4)
// per-direction estimate (ADR-0045). An [llm.EventError] or a stream that closes
// without an [llm.EventDone] fails the call — a truncated response is never
// presented as complete — but any provider-REPORTED usage that already arrived
// is still metered on those paths (ADR-0045's error rule: a partial turn is
// metered by what was reported, not by a fabricated estimate — matching
// agenttool).
func (c *Caller) Call(system, user string, maxTokens int) (string, error) {
	stream, err := c.Provider.Complete(c.Ctx, llm.Request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: system},
			{Role: llm.RoleUser, Text: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("%s: llm complete: %w", c.ErrPrefix, err)
	}

	var sb strings.Builder
	var usage llm.Usage
	var haveUsage, done bool
	var streamErr error
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			sb.WriteString(ev.Text)
		case llm.EventUsage:
			usage, haveUsage = ev.Usage, true
		case llm.EventDone:
			done = true
		case llm.EventError:
			streamErr = errors.New(ev.Err)
		}
	}
	if streamErr != nil {
		c.meterReported(haveUsage, usage)
		return "", fmt.Errorf("%s: llm stream error: %w", c.ErrPrefix, streamErr)
	}
	if !done {
		c.meterReported(haveUsage, usage)
		if err := c.Ctx.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%s: completion stream ended without a done event (truncated response)", c.ErrPrefix)
	}

	text := sb.String()
	c.meter(haveUsage, usage, system+user, text)
	return text, nil
}

// meter records a COMPLETED call's token usage: the provider-reported counts, or
// a ceil(chars/4) per-direction estimate over the sent prompt and received text
// when none was reported. The input estimate is always >0 (the system+user
// prompt is non-empty); a completed call that returned no text meters out=0,
// matching agenttool. PriceModel rides only to the sink for pricing (ADR-0046);
// Prometheus drops it (ADR-0032).
func (c *Caller) meter(haveUsage bool, u llm.Usage, sent, received string) {
	in, out := u.InputTokens, u.OutputTokens
	if !haveUsage {
		in = EstimateTokens(utf8.RuneCountInString(sent))
		out = EstimateTokens(utf8.RuneCountInString(received))
	}
	c.record(in, out)
}

// meterReported records ONLY provider-reported usage (the error/truncation rule,
// ADR-0045): a failed or truncated call is metered by what the provider actually
// reported, never a fabricated estimate.
func (c *Caller) meterReported(haveUsage bool, u llm.Usage) {
	if haveUsage {
		c.record(u.InputTokens, u.OutputTokens)
	}
}

func (c *Caller) record(in, out int) {
	c.Rec.LLMTokens(c.Label, c.PriceModel, in, out)
	c.TotalIn += in
	c.TotalOut += out
}

// EstimateTokens is the ceil(chars/4) per-direction token estimate (ADR-0045).
func EstimateTokens(runes int) int { return (runes + 3) / 4 }

// ProviderLabel maps a provider_config id to the bounded [observe.Provider]
// metric label; the empty id (default) is Groq (ADR-0036). The wired ids equal
// their observe constants, so the cast is exact.
func ProviderLabel(providerID string) observe.Provider {
	if providerID == "" {
		return observe.ProviderGroq
	}
	return observe.Provider(providerID)
}
