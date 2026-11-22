package anthropic

import "strings"

// This file is prompt caching for the voice model (#702).
//
// A voice prompt has a session-constant head — the tool declarations and the
// stable system prompt (Persona, roster, output rules; ADR-0059) — and every
// request of a session resends it: each tool round, each turn. With a cache
// breakpoint at the end of the stable system prompt, the API bills that head at
// a tenth of the input rate whenever the previous request was less than five
// minutes ago, against a 25% surcharge on the request that writes it. One read
// per write already pays for the write, and a tool turn alone is a write
// followed by a read seconds later, so for a live session this is a cost
// reduction. It was measured to make no difference to latency at this prompt
// size.
//
// The breakpoint goes on the FIRST system message only. Everything after it —
// the volatile tail the Agent loop appends per turn (facts, recalled memory, a
// directive) — rides a second block without one, so its changes never touch
// the cached prefix.
//
// It is gated per model, like the thinking policy: the request of every other
// model keeps its plain system string, byte for byte.
//
// Metering: the API reports cached tokens outside input_tokens; the adapter
// adds them back ([usageWire.promptTokens]) and the spend table prices them at
// the full input rate. Caching therefore lowers the real bill, not the metered
// estimate, which stays an upper bound.

// DefaultPromptCaching is the adapter's built-in gate: prompt caching is on for
// claude-haiku-5-5 (the voice model; it caches a prefix of 512 tokens or more)
// and off for everything else. claude-haiku-4-5 only caches a prefix of 4096
// tokens or more, which a voice prompt does not reach.
func DefaultPromptCaching(model string) bool {
	return strings.HasPrefix(model, HaikuVoiceModel)
}

// WithPromptCachingFor replaces the per-model prompt-caching gate
// ([DefaultPromptCaching]). A nil gate is ignored.
func WithPromptCachingFor(gate func(model string) bool) Option {
	return func(c *Client) {
		if gate != nil {
			c.caching = gate
		}
	}
}

// systemBlock is one text block of the array form of the `system` field.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl is a cache breakpoint; "ephemeral" is the 5-minute cache.
type cacheControl struct {
	Type string `json:"type"`
}

// wireSystem renders the system messages' texts as the request's `system`
// field. Without caching it is the texts joined by blank lines, as one string
// (nil when there are none, so the field is omitted). With caching it is an
// array: the first text as the cached block, the rest joined as a second.
func (c *Client) wireSystem(model string, parts []string) any {
	if len(parts) == 0 {
		return nil
	}
	if !c.caching(model) {
		return strings.Join(parts, "\n\n")
	}
	blocks := []systemBlock{{Type: "text", Text: parts[0], CacheControl: &cacheControl{Type: "ephemeral"}}}
	if len(parts) > 1 {
		blocks = append(blocks, systemBlock{Type: "text", Text: strings.Join(parts[1:], "\n\n")})
	}
	return blocks
}
