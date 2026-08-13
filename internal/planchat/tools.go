package planchat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// The chat tool belt (ADR-0062): search_transcripts / find_node /
// appearances_of over the #591 retrieval engine, propose_knowledge over the
// ADR-0052 proposal path, and recap over #271's generator. Each tool is built
// per exchange, bound to the exchange's Campaign from SERVER state — the LLM
// never names a campaign (ADR-0029). The chat is a GM-only surface, so reads
// run at [search.AccessOperator] and gm_private content may appear in results;
// nothing here reaches a player.

// Result budgets: a tool result lands in the conversation and is paid for on
// every following round, so each is bounded (the RecapResultBudgetRunes
// discipline).
const (
	defaultSearchLimit  = 8
	maxSearchLimit      = 20
	defaultNodeLimit    = 5
	maxNodeLimit        = 10
	nodeBodyExcerptMax  = 600
	toolResultBudget    = 4000
	defaultAppearances  = 10
	transcriptQueryMax  = 300
	appearancesQueryMax = 120
)

// limitArg clamps an LLM-supplied limit onto [1, max], with fallback for zero.
func limitArg(v, fallback, max int) int {
	if v <= 0 {
		return fallback
	}
	if v > max {
		return max
	}
	return v
}

// capRunes truncates s to at most n runes, appending an ellipsis when trimmed.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// stamp renders a result timestamp compactly (UTC date); the chat surface
// localizes nothing server-side — these ride inside LLM prose.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "unknown date"
	}
	return t.UTC().Format("2006-01-02")
}

// --- search_transcripts -----------------------------------------------------

// transcriptSearcher is the #591 engine surface the tool needs; *search.Engine
// satisfies it.
type transcriptSearcher interface {
	SearchTranscripts(ctx context.Context, campaignID uuid.UUID, access search.Access, query string, limit int) (search.TranscriptResults, error)
}

type searchTranscriptsTool struct {
	eng        transcriptSearcher
	campaignID uuid.UUID
}

func (searchTranscriptsTool) Name() string { return "search_transcripts" }

func (searchTranscriptsTool) Description() string {
	return "Search everything said in this campaign's past voice sessions. " +
		"Returns matching transcript passages with speaker and date. " +
		"Use it to check what actually happened before answering from memory."
}

func (searchTranscriptsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What to search for — a phrase, name, or topic."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "description": "How many passages to return (default 8)."}
  },
  "required": ["query"]
}`)
}

func (searchTranscriptsTool) ReadOnly() bool      { return true }
func (searchTranscriptsTool) SupportsScope() bool { return false }

func (t searchTranscriptsTool) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("search_transcripts: invalid arguments: %w", err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return "", fmt.Errorf("search_transcripts: query must not be empty")
	}
	query = capRunes(query, transcriptQueryMax)

	res, err := t.eng.SearchTranscripts(ctx, t.campaignID, search.AccessOperator, query, limitArg(a.Limit, defaultSearchLimit, maxSearchLimit))
	if err != nil {
		return "", fmt.Errorf("search_transcripts: %w", err)
	}
	if len(res.Hits) == 0 {
		return "No transcript passages matched.", nil
	}
	var b strings.Builder
	for _, h := range res.Hits {
		line := fmt.Sprintf("- [%s] %s: %s\n", stamp(h.At), h.Who, h.Snippet)
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line) > toolResultBudget {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// --- find_node --------------------------------------------------------------

// nodeSearcher is the #591 engine surface the tool needs; *search.Engine
// satisfies it.
type nodeSearcher interface {
	SearchNodes(ctx context.Context, campaignID uuid.UUID, access search.Access, query string, limit int) ([]storage.KGNode, error)
}

type findNodeTool struct {
	eng        nodeSearcher
	campaignID uuid.UUID
}

func (findNodeTool) Name() string { return "find_node" }

func (findNodeTool) Description() string {
	return "Look up entries in this campaign's knowledge graph (characters, NPCs, locations, factions, items, plot threads, notes) by name or topic. " +
		"Returns each match's name, type, and content."
}

func (findNodeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "The entry name or topic to look up."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 10, "description": "How many entries to return (default 5)."}
  },
  "required": ["query"]
}`)
}

func (findNodeTool) ReadOnly() bool      { return true }
func (findNodeTool) SupportsScope() bool { return false }

func (t findNodeTool) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("find_node: invalid arguments: %w", err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return "", fmt.Errorf("find_node: query must not be empty")
	}
	query = capRunes(query, appearancesQueryMax)

	nodes, err := t.eng.SearchNodes(ctx, t.campaignID, search.AccessOperator, query, limitArg(a.Limit, defaultNodeLimit, maxNodeLimit))
	if err != nil {
		return "", fmt.Errorf("find_node: %w", err)
	}
	if len(nodes) == 0 {
		return "No entries matched.", nil
	}
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(formatNode(n))
		if utf8.RuneCountInString(b.String()) > toolResultBudget {
			break
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// formatNode renders one KG entry for the model: name, type, GM-only marker,
// aspects (the chat is a GM-only surface, so private aspects are included),
// and a bounded body excerpt.
func formatNode(n storage.KGNode) string {
	var b strings.Builder
	b.WriteString("## ")
	b.WriteString(n.Name)
	b.WriteString(" (")
	b.WriteString(string(n.Type))
	if n.GMPrivate {
		b.WriteString(", GM-only")
	}
	b.WriteString(")\n")
	for _, a := range n.Aspects {
		v := strings.TrimSpace(a.Value)
		if v == "" {
			continue
		}
		b.WriteString("- ")
		if a.Key != "" {
			b.WriteString(a.Key)
			b.WriteString(": ")
		}
		b.WriteString(capRunes(v, nodeBodyExcerptMax))
		b.WriteString("\n")
	}
	if body := strings.TrimSpace(n.Body); body != "" {
		b.WriteString(capRunes(body, nodeBodyExcerptMax))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// --- appearances_of ---------------------------------------------------------

// appearanceLister is the storage read behind appearances_of; *storage.Store
// satisfies it.
type appearanceLister interface {
	ListNodeAppearances(ctx context.Context, campaignID, nodeID uuid.UUID, limit int) ([]storage.AppearanceHit, error)
}

type appearancesOfTool struct {
	eng        nodeSearcher
	store      appearanceLister
	campaignID uuid.UUID
}

func (appearancesOfTool) Name() string { return "appearances_of" }

func (appearancesOfTool) Description() string {
	return "Find when a campaign entity (NPC, location, item, …) was last mentioned in a voice session: " +
		"returns its most recent transcript mentions, newest first, with what was said."
}

func (appearancesOfTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The entity's name as it appears in the knowledge graph."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "description": "How many mentions to return (default 10)."}
  },
  "required": ["name"]
}`)
}

func (appearancesOfTool) ReadOnly() bool      { return true }
func (appearancesOfTool) SupportsScope() bool { return false }

func (t appearancesOfTool) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	var a struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("appearances_of: invalid arguments: %w", err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", fmt.Errorf("appearances_of: name must not be empty")
	}
	name = capRunes(name, appearancesQueryMax)

	// Resolve the name to a Node via the same search the GM's palette uses; the
	// top hit is the entity, the runners-up are disambiguation hints.
	nodes, err := t.eng.SearchNodes(ctx, t.campaignID, search.AccessOperator, name, 3)
	if err != nil {
		return "", fmt.Errorf("appearances_of: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Sprintf("No knowledge graph entry matched %q.", name), nil
	}
	node := nodes[0]

	hits, err := t.store.ListNodeAppearances(ctx, t.campaignID, node.ID, limitArg(a.Limit, defaultAppearances, storage.MaxAppearances))
	if err != nil {
		return "", fmt.Errorf("appearances_of: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Appearances of %s (%s), newest first:\n", node.Name, node.Type)
	if len(hits) == 0 {
		fmt.Fprintf(&b, "None recorded — %s has not been mentioned in any voice session.\n", node.Name)
	}
	for _, h := range hits {
		line := fmt.Sprintf("- [%s] %s: %s\n", stamp(h.SessionStartedAt), h.Who, capRunes(h.Text, search.SnippetRunes))
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line) > toolResultBudget {
			break
		}
		b.WriteString(line)
	}
	if len(nodes) > 1 {
		others := make([]string, 0, len(nodes)-1)
		for _, n := range nodes[1:] {
			others = append(others, n.Name)
		}
		fmt.Fprintf(&b, "(Other matching entries: %s.)\n", strings.Join(others, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// --- propose_knowledge ------------------------------------------------------

// proposeKnowledgeTool is [tool.RememberKnowledge] under the chat belt's name
// (ADR-0062): identical validation, dedup, and proposal mechanics — the writer
// it wraps is campaign-stamped by the exchange ctx (knowledge.WithCampaign),
// and the grant row carries the Butler's campaign-wide scope (ADR-0052).
type proposeKnowledgeTool struct {
	*tool.RememberKnowledge
}

func (proposeKnowledgeTool) Name() string { return "propose_knowledge" }

func (proposeKnowledgeTool) Description() string {
	return "Propose a new fact, relationship or entry for this campaign's knowledge graph. " +
		"It is saved to the Proposals queue as a pending suggestion — nothing becomes canon until the GM approves it there. " +
		"Only propose genuinely NEW knowledge: never re-propose something already recorded or already pending, and do not " +
		"re-propose the same fact phrased differently."
}

// --- recap ------------------------------------------------------------------

// chatRecapTool is [tool.Recap] with a chat-appropriate description: in this
// surface the reader IS the GM, and nothing is spoken aloud.
type chatRecapTool struct {
	*tool.Recap
}

func (chatRecapTool) Description() string {
	return "Summarize what happened in this campaign's most recent ended voice session(s). " +
		"Use when the GM asks what happened last time, or to ground next-session planning in recent events."
}

// chatTools builds the exchange's tool registry, every tool bound to the
// exchange's Campaign. The registry carries the full belt; which tools the
// model may actually see and call is the grant rows' decision (ADR-0029).
func (e *Engine) chatTools(campaignID uuid.UUID) (*tool.Registry, error) {
	reg := tool.NewRegistry()
	for _, t := range []tool.Tool{
		searchTranscriptsTool{eng: e.search, campaignID: campaignID},
		findNodeTool{eng: e.search, campaignID: campaignID},
		appearancesOfTool{eng: e.search, store: e.store, campaignID: campaignID},
		proposeKnowledgeTool{tool.NewRememberKnowledge(e.writer)},
		chatRecapTool{tool.NewRecap(e.recapper)},
	} {
		if err := reg.Register(t); err != nil {
			return nil, fmt.Errorf("planchat: register %s: %w", t.Name(), err)
		}
	}
	return reg, nil
}
