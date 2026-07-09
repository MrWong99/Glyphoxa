package recap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// recapInstruction is the Butler-flavoured recap directive appended after the
// Butler's Persona. Fixed text: the cassette prompt hash (ADR-0021) depends on it.
const recapInstruction = "You are recapping a past tabletop RPG voice session for the players. " +
	"Summarize what happened as a single coherent narrative recap in your own voice, " +
	"preserving the key events, characters, and decisions in the order they occurred. " +
	"Do not invent details that are not in the transcript."

// neutralInstruction is the map-step directive: a plain factual condensation with no
// persona, used per window before the Butler-flavoured reduce.
const neutralInstruction = "Condense the following transcript excerpt into a factual, concise summary. " +
	"Preserve the key events, characters, names, and decisions in order. " +
	"Do not add commentary, flavor, or details not present in the excerpt."

// answerLanguageLine, when a Campaign Language is set, pins the output language.
func answerLanguageLine(language string) string {
	if language == "" {
		return ""
	}
	return "\n\nAnswer in " + language + "."
}

// butlerSystemPrompt is the Persona-flavoured system prompt for a single-call recap
// or the reduce step: Persona + recap instruction + language pin.
func butlerSystemPrompt(persona, language string) string {
	var b strings.Builder
	if persona != "" {
		b.WriteString(persona)
		b.WriteString("\n\n")
	}
	b.WriteString(recapInstruction)
	b.WriteString(answerLanguageLine(language))
	return b.String()
}

// neutralSystemPrompt is the persona-free factual system prompt for the map step.
func neutralSystemPrompt(language string) string {
	return neutralInstruction + answerLanguageLine(language)
}

// llmCaller drives one recap's LLM completions against a fixed provider/model,
// metering each via rec and accumulating the token totals for the attribution log.
//
// model is the request model (empty lets the adapter pick its default). priceModel
// is the model the spend sink prices on: it equals model except on the default path,
// where model is "" but priceModel is groq.DefaultModel, so (groq, "") is never a
// price-map miss (#272 review). The request model stays "" so the cassette hash and
// the adapter default are unchanged.
type llmCaller struct {
	ctx        context.Context
	provider   llm.Provider
	model      string
	priceModel string
	label      observe.Provider
	rec        observe.StageRecorder

	totalIn  int
	totalOut int
}

// call runs one completion (system + user), drains it to close, and returns the
// accumulated text. Usage is metered from the provider-reported [llm.EventUsage] or,
// when none arrives on a COMPLETED call, a documented ceil(chars/4) per-direction
// estimate (ADR-0045). An [llm.EventError] or a stream that closes without an
// [llm.EventDone] fails the call — a truncated recap is never presented as complete
// — but any provider-REPORTED usage that already arrived is still metered on those
// paths (ADR-0045's error rule: a partial turn is metered by what was reported, not
// by a fabricated estimate — matching agenttool).
func (c *llmCaller) call(system, user string, maxTokens int) (string, error) {
	stream, err := c.provider.Complete(c.ctx, llm.Request{
		Model:     c.model,
		MaxTokens: maxTokens,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: system},
			{Role: llm.RoleUser, Text: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("recap: llm complete: %w", err)
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
		return "", fmt.Errorf("recap: llm stream error: %w", streamErr)
	}
	if !done {
		c.meterReported(haveUsage, usage)
		if err := c.ctx.Err(); err != nil {
			return "", err
		}
		return "", errors.New("recap: completion stream ended without a done event (truncated response)")
	}

	text := sb.String()
	c.meter(haveUsage, usage, system+user, text)
	return text, nil
}

// meter records a COMPLETED call's token usage: the provider-reported counts, or a
// ceil(chars/4) per-direction estimate over the sent prompt and received text when
// none was reported. The input estimate is always >0 (the system+user prompt is
// non-empty); a completed call that returned no text meters out=0, matching
// agenttool. priceModel rides only to the sink for pricing (ADR-0046); Prometheus
// drops it (ADR-0032).
func (c *llmCaller) meter(haveUsage bool, u llm.Usage, sent, received string) {
	in, out := u.InputTokens, u.OutputTokens
	if !haveUsage {
		in = estimateTokens(utf8.RuneCountInString(sent))
		out = estimateTokens(utf8.RuneCountInString(received))
	}
	c.record(in, out)
}

// meterReported records ONLY provider-reported usage (the error/truncation rule,
// ADR-0045): a failed or truncated call is metered by what the provider actually
// reported, never a fabricated estimate.
func (c *llmCaller) meterReported(haveUsage bool, u llm.Usage) {
	if haveUsage {
		c.record(u.InputTokens, u.OutputTokens)
	}
}

func (c *llmCaller) record(in, out int) {
	c.rec.LLMTokens(c.label, c.priceModel, in, out)
	c.totalIn += in
	c.totalOut += out
}

// estimateTokens is the ceil(chars/4) per-direction token estimate (ADR-0045).
func estimateTokens(runes int) int { return (runes + 3) / 4 }
