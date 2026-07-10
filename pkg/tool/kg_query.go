package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxKGFactBodyRunes caps one rendered KG fact's body length, in runes (mirrors
// kgfacts.MaxFactChars). The whole result is still bounded by MaxToolResultChars.
const MaxKGFactBodyRunes = 500

// MaxKGNameRunes caps a fact's Node-name length, in runes (mirrors
// kgfacts.MaxNameChars): without it a single pathological name could push the
// first block past the whole-result budget and, since the budget stop is a
// deterministic prefix, silently drop every fact.
const MaxKGNameRunes = 200

// Scope config values (S3, ADR-0029). The grant's jsonb config is {"scope":…};
// the direction sets the default when the config is absent.
const (
	scopeOwnNode  = "own_node"
	scopeCampaign = "campaign"
)

// scopeConfig is the decoded per-grant scope (S3). A grant with no config leaves
// Scope "", and the handler applies the direction's default.
type scopeConfig struct {
	Scope string `json:"scope"`
}

// parseScope decodes the per-grant config into a scope string. The config
// arrives from a hydrated tool_agent_grant jsonb row as json.RawMessage (or nil
// for an unscoped grant); a []byte or string is accepted defensively. An empty
// or absent config yields "" so the caller applies its direction default. A
// present-but-unknown scope value is an error — a misconfigured grant must fail
// loudly, not silently widen.
func parseScope(grantConfig any) (string, error) {
	var raw []byte
	switch c := grantConfig.(type) {
	case nil:
		return "", nil
	case json.RawMessage:
		raw = c
	case []byte:
		raw = c
	case string:
		raw = []byte(c)
	default:
		return "", fmt.Errorf("unrecognized grant config type %T", grantConfig)
	}
	if len(raw) == 0 {
		return "", nil
	}
	var sc scopeConfig
	if err := json.Unmarshal(raw, &sc); err != nil {
		return "", fmt.Errorf("invalid scope config: %w", err)
	}
	switch sc.Scope {
	case "", scopeOwnNode, scopeCampaign:
		return sc.Scope, nil
	default:
		return "", fmt.Errorf("unknown scope %q", sc.Scope)
	}
}

// KGQuery is the read-only kg_query built-in (#296): a lookup over the Knowledge
// Graph's Node read paths (ADR-0008). It is the canonical ADR-0029 scope-narrowing
// example — the SAME registered Tool reads a different slice per Agent purely via
// the grant config, enforced HERE in the handler, never by the LLM:
//
//   - own_node (the default for a Character NPC's grant): only the caller's own
//     linked Node and its single-hop neighbourhood, gm_private already filtered.
//     The caller is read from the turn ctx ([CallerID]), NOT the LLM's args — so
//     no crafted argument can widen the NPC to another Node's neighbourhood.
//   - campaign (the Butler's grant): a relevance search across the whole
//     Campaign's public Nodes.
//
// nil grant config defaults to campaign for this read direction (S3): a read is
// gm_private-filtered either way, so the wider default is safe; a write Tool
// fails closed to own_node instead. A nil source reports unavailable at Execute.
type KGQuery struct {
	src KGReader
}

// NewKGQuery builds the Tool over src. A nil src registers the Tool but reports
// unavailable at Execute time.
func NewKGQuery(src KGReader) *KGQuery {
	return &KGQuery{src: src}
}

// Name implements [Tool].
func (*KGQuery) Name() string { return "kg_query" }

// Description implements [Tool].
func (*KGQuery) Description() string {
	return "Look up what you know about the world: people, places, factions, items and plot threads. " +
		"Use it to recall facts before answering."
}

// InputSchema implements [Tool]. It shares the query/limit schema with
// transcript_search; the scope is NEVER an argument (it lives in the grant).
func (*KGQuery) InputSchema() json.RawMessage { return searchInputSchema }

// ReadOnly implements [Tool]: a KG lookup mutates no state (ADR-0030).
func (*KGQuery) ReadOnly() bool { return true }

// SupportsScope implements [Tool]: kg_query's authority is narrowed per Agent via
// the grant config (own_node vs campaign), so the grant editor renders its scope
// UI (ADR-0029).
func (*KGQuery) SupportsScope() bool { return true }

// Execute implements [Tool]. It resolves the effective scope from grantConfig
// (never the args), reads the corresponding KG slice, and renders the facts for
// the prompt. own_node reads the CALLER's neighbourhood ([CallerID]) filtered to
// the query terms; campaign runs the relevance search. A nil source yields the
// unavailable error; no facts yields a friendly "none" line, not an error.
func (kq *KGQuery) Execute(ctx context.Context, args json.RawMessage, grantConfig any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if kq.src == nil {
		return "", fmt.Errorf("kg_query: knowledge graph is unavailable in this mode")
	}
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("kg_query: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("kg_query: query must not be empty")
	}

	scope, err := parseScope(grantConfig)
	if err != nil {
		return "", fmt.Errorf("kg_query: %w", err)
	}
	if scope == "" {
		scope = scopeCampaign // read direction default (S3): reads are gm_private-filtered
	}

	var facts []KGFact
	switch scope {
	case scopeOwnNode:
		// Own-node scope is the caller's neighbourhood, resolved from the turn ctx —
		// never the LLM args, so crafted arguments cannot widen it. An unstamped ctx
		// (CallerID "") has no neighbourhood to scope to and reads nothing.
		facts, err = kq.src.OwnNodeFacts(ctx, CallerID(ctx))
		if err != nil {
			return "", fmt.Errorf("kg_query: %w", err)
		}
		facts = filterByQuery(facts, a.Query)
	default: // scopeCampaign
		facts, err = kq.src.SearchFacts(ctx, a.Query, clampedLimit(a.Limit))
		if err != nil {
			return "", fmt.Errorf("kg_query: %w", err)
		}
	}
	return renderKGFacts(facts), nil
}

// filterByQuery narrows own-node facts to those whose Name or Body contains any
// of the query's terms (case-insensitive), so an own_node kg_query answers the
// question asked rather than dumping the whole neighbourhood. A query with no
// alphanumeric terms matches everything (the neighbourhood is already tiny and
// caller-scoped).
func filterByQuery(facts []KGFact, query string) []KGFact {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !isAlnum(r)
	})
	if len(terms) == 0 {
		return facts
	}
	out := facts[:0:0]
	for _, f := range facts {
		hay := strings.ToLower(f.Name + " " + f.Body)
		for _, term := range terms {
			if strings.Contains(hay, term) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// renderKGFacts projects facts into "### Name (Type)\nBody" blocks, each body
// rune-truncated to MaxKGFactBodyRunes, the whole result bounded to
// MaxToolResultChars with a deterministic prefix-stop. An empty set renders the
// "none" line.
func renderKGFacts(facts []KGFact) string {
	if len(facts) == 0 {
		return "no matching knowledge"
	}
	var b strings.Builder
	n := 0
	for _, f := range facts {
		head := fmt.Sprintf("### %s (%s)", truncateRunes(f.Name, MaxKGNameRunes), f.Type)
		body := truncateRunes(strings.TrimSpace(f.Body), MaxKGFactBodyRunes)
		block := head
		if body != "" {
			block = head + "\n" + body
		}
		add := block
		if n > 0 {
			add = "\n\n" + block
		}
		if b.Len()+len(add) > MaxToolResultChars {
			break
		}
		b.WriteString(add)
		n++
	}
	if n == 0 {
		return "no matching knowledge"
	}
	return b.String()
}
