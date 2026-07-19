package assist

import (
	"fmt"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// campaignContextLines renders the shared campaign framing both drafts open
// with: the campaign's name and System, so the model writes in-setting and
// rules-aware prose.
func campaignContextLines(c storage.Campaign) string {
	var b strings.Builder
	if c.Name != "" {
		b.WriteString("\nThe campaign is called \"" + c.Name + "\".")
	}
	if c.System != "" {
		b.WriteString("\nThe TTRPG system is " + c.System + ".")
	}
	return b.String()
}

// writeLanguageLine, when a Campaign Language is set, pins the draft language —
// generated content is world material, so it follows the Campaign Language like
// the recap does, not the (English) operator UI.
func writeLanguageLine(language string) string {
	if language == "" {
		return ""
	}
	return "\n\nWrite in " + language + "."
}

// personaInstruction is the fixed persona-draft directive. A Persona is the
// markdown description injected into the Agent's LLM prompts (CONTEXT.md), so
// the draft is written as a directly usable second-person system prompt.
const personaInstruction = "You are helping a tabletop-RPG game master flesh out a non-player character " +
	"(NPC) for their campaign. From the GM's short description, write the NPC's Persona: a Markdown " +
	"description of the character's personality, backstory, motivations, and speech style, addressed to " +
	"the character in the second person (\"You are …\") so it can be used directly as that character's " +
	"system prompt. Keep it under 300 words. Output ONLY the persona markdown — no preamble, no headings " +
	"about the task, no code fences."

// personaSystemPrompt is the full system prompt for a persona draft.
func personaSystemPrompt(c storage.Campaign) string {
	return personaInstruction + campaignContextLines(c) + writeLanguageLine(c.Language)
}

// personaUserPrompt renders the GM's description plus the editor's current
// name/title so the draft addresses the right character. A name the GM has not
// set yet is simply absent — the model may then coin one inside the persona.
func personaUserPrompt(in PersonaInput) string {
	var b strings.Builder
	b.WriteString(in.Prompt)
	if n := strings.TrimSpace(in.AgentName); n != "" {
		b.WriteString("\n\nThe character's name is " + n + ".")
	}
	if t := strings.TrimSpace(in.AgentTitle); t != "" {
		b.WriteString("\nTheir title is " + t + ".")
	}
	return b.String()
}

// knowledgeInstruction is the fixed knowledge-draft directive: the strict-JSON
// contract parseDraft consumes. The node/edge vocabularies and the object-side
// edge matrix are spelled out so a well-behaved model produces a draft that
// survives validation unchanged.
const knowledgeInstruction = "You are helping a tabletop-RPG game master build their campaign's knowledge " +
	"graph: a wiki of typed entries (nodes) connected by typed, directional relationships (edges). From " +
	"the GM's request, draft a coherent, interlinked set of new entries.\n" +
	"Respond with ONLY one JSON object — no code fences, no commentary — of exactly this shape:\n" +
	`{"nodes":[{"type":"npc","name":"…","body":"…","gm_private":false}],"edges":[{"from":0,"to":1,"type":"resides_in"}]}` + "\n" +
	"Rules:\n" +
	"- \"type\" of a node must be one of: %s.\n" +
	"- \"type\" of an edge must be one of: %s. Edges read from→to (e.g. from resides_in to).\n" +
	"- resides_in must point AT a location node; member_of AT a faction node; participated_in AT a " +
	"plot_thread node; parent_of connects only character/npc nodes.\n" +
	"- \"from\"/\"to\" are zero-based indices into YOUR OWN nodes list; never reference anything else.\n" +
	"- Create between 2 and %d nodes, linked among themselves wherever the fiction supports it.\n" +
	"- \"body\" is 2–5 sentences of world lore — what the world knows about the entry.\n" +
	"- Set \"gm_private\" true only for entries the players must not learn (twists, secrets).\n" +
	"- Do not duplicate the campaign's existing entries; invent new material that fits alongside them."

// knowledgeSystemPrompt is the full system prompt for a knowledge draft:
// directive + campaign framing + existing-entry names (duplicate avoidance) +
// language pin. gm_private entries are omitted from the name list defensively —
// their existence stays off every prompt this codebase assembles.
func knowledgeSystemPrompt(c storage.Campaign, existing []storage.KGNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, knowledgeInstruction,
		strings.Join(kgvocab.NodeTypes(), ", "),
		strings.Join(kgvocab.Relations(), ", "),
		maxDraftNodes)
	b.WriteString(campaignContextLines(c))

	names := make([]string, 0, min(len(existing), maxContextNames))
	for _, n := range existing {
		if n.GMPrivate {
			continue
		}
		if len(names) == maxContextNames {
			break
		}
		names = append(names, n.Name+" ("+string(n.Type)+")")
	}
	if len(names) > 0 {
		b.WriteString("\n\nExisting entries (do NOT duplicate): " + strings.Join(names, ", ") + ".")
	}

	b.WriteString(writeLanguageLine(c.Language))
	return b.String()
}
