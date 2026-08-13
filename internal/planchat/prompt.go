package planchat

import (
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// The chat prompt layout (ADR-0062): a STABLE prefix — Persona + chat-tool
// instructions, nothing per-exchange — followed by the thread history, then
// the new user message. The ADR-0059 voice layout deliberately does not carry
// over: no speaker roster, no volatile facts/memory/location/directive tail.
// World knowledge arrives through the tool calls instead of pushed prompt
// blocks; that keeps the prefix byte-stable across every exchange of every
// thread for provider-side prompt caching.

// historyCharBudget bounds how much thread history one exchange carries into
// the prompt, in runes over the message contents, mirroring the recap engine's
// single-call budget scale. The newest messages win; the oldest fall off. No
// summarization in v1 — a thread past the budget simply forgets its start,
// which is the documented residual.
const historyCharBudget = 24_000

// historyMessageCap bounds the history by count as well: many tiny messages
// carry per-message overhead a pure rune budget does not see.
const historyMessageCap = 60

// chatInstructions is the tool-belt briefing appended to the Persona in the
// stable prefix. It names the chat's job (prep between sessions, GM-facing)
// and the grounding rules; the tool declarations themselves ride the request's
// tool list, not this text.
const chatInstructions = `

You are chatting with this campaign's GM in the web app between sessions, helping with prep and planning: recalling what happened in past voice sessions, tracking plot threads, and drafting ideas for the next session.

Ground your answers in the campaign's actual records using your tools: search_transcripts for what was said in past sessions, find_node for the campaign's wiki entries, appearances_of for when an entity last came up, and recap for a summary of recent sessions. Prefer looking things up over guessing; when the records are silent, say so and mark your own inventions clearly as suggestions.

When the GM asks you to note something down — or you have a concrete fact worth keeping — use propose_knowledge; it lands in the Proposals queue for the GM to approve, so propose freely but never claim something is already recorded when it is only proposed. Answer in the language the GM writes in.`

// stablePrefix renders the chat system prompt for a Butler: Persona first
// (verbatim, as the voice prompt does), then the chat instructions. Everything
// in it derives from the Agent row alone, so it only changes when the GM edits
// the Butler.
func stablePrefix(butler storage.Agent) string {
	var b strings.Builder
	b.WriteString("You are ")
	b.WriteString(butler.Name)
	if butler.Title != "" {
		b.WriteString(", ")
		b.WriteString(butler.Title)
	}
	b.WriteString(", this campaign's assistant.")
	if p := strings.TrimSpace(butler.Persona); p != "" {
		b.WriteString("\n\n")
		b.WriteString(p)
	}
	b.WriteString(chatInstructions)
	return b.String()
}

// buildMessages assembles the exchange's conversation: the stable prefix, the
// budget-trimmed thread history, then the new user message. history is in seq
// order; the newest messages are kept when the budget trims.
func buildMessages(butler storage.Agent, history []storage.PlanningMessage, userText string) []tool.Message {
	kept := trimHistory(history)
	msgs := make([]tool.Message, 0, len(kept)+2)
	msgs = append(msgs, tool.Message{Role: tool.RoleSystem, Text: stablePrefix(butler)})
	for _, m := range kept {
		role := tool.RoleUser
		if m.Role == storage.PlanningRoleAssistant {
			role = tool.RoleAssistant
		}
		msgs = append(msgs, tool.Message{Role: role, Text: m.Content})
	}
	msgs = append(msgs, tool.Message{Role: tool.RoleUser, Text: userText})
	return msgs
}

// trimHistory keeps the newest messages within both budgets, preserving order.
func trimHistory(history []storage.PlanningMessage) []storage.PlanningMessage {
	if len(history) == 0 {
		return nil
	}
	start := len(history)
	budget := historyCharBudget
	for start > 0 && len(history)-start < historyMessageCap {
		next := history[start-1]
		cost := utf8.RuneCountInString(next.Content)
		if cost > budget {
			break
		}
		budget -= cost
		start--
	}
	return history[start:]
}
