package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// MaxProposalTextRunes caps the free-text (prose) fields a remember_knowledge
// proposal carries — fact and body — in runes: a pathological wall of text has no
// business in the GM's review queue. The shorter entity-name fields (subject,
// target, name) are capped at MaxKGNameRunes instead. Together these two caps
// bound EVERY field, so one proposed_write's jsonb is bounded. Enforced in the
// handler, per-field, before the writer is ever called.
const MaxProposalTextRunes = 2000

// Proposal kinds (ADR-0052). fact/edge may be proposed own_node or campaign;
// node (a brand-new wiki entry) is Butler-only (campaign scope).
const (
	proposalKindFact = "fact"
	proposalKindEdge = "edge"
	proposalKindNode = "node"
)

// proposalWriteVersion is the schema version stamped onto every ProposedWrite
// (ADR-0052 "v":1), so a later shape change is detectable on the stored jsonb.
const proposalWriteVersion = 1

// relationValues is the closed set of Edge relations an Agent may propose,
// mirroring the kg_edge relation vocabulary (ADR-0008). Exact lowercase
// snake_case — the review surface and the eventual approved write depend on it.
var relationValues = map[string]bool{
	"resides_in":      true,
	"member_of":       true,
	"owns":            true,
	"knows":           true,
	"enemy_of":        true,
	"ally_of":         true,
	"parent_of":       true,
	"participated_in": true,
	"mentioned_in":    true,
}

// nodeTypeValues is the closed set of Node types a Butler may propose for a new
// entry, mirroring storage.KGNodeType (ADR-0008). Exact lowercase snake_case.
var nodeTypeValues = map[string]bool{
	"character":   true,
	"npc":         true,
	"location":    true,
	"faction":     true,
	"item":        true,
	"plot_thread": true,
	"note":        true,
}

// rememberKnowledgeInputSchema is the JSON Schema declared to the LLM. Scope is
// NEVER an argument (ADR-0029: it lives in the grant, enforced in the handler);
// the model only ever names WHAT it wants to remember, never on whose authority.
var rememberKnowledgeInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {
      "type": "string",
      "enum": ["fact", "edge", "node"],
      "description": "fact: a statement about an entity. edge: a relationship between two entities. node: a brand-new entry for the world."
    },
    "subject": {
      "type": "string",
      "description": "The name of the entity the fact or edge is about (for a campaign-wide proposal). Ignored when you may only write about yourself."
    },
    "fact": {
      "type": "string",
      "description": "The fact to remember about the subject (kind=fact)."
    },
    "relation": {
      "type": "string",
      "enum": ["resides_in", "member_of", "owns", "knows", "enemy_of", "ally_of", "parent_of", "participated_in", "mentioned_in"],
      "description": "The relationship type (kind=edge)."
    },
    "target": {
      "type": "string",
      "description": "The name of the entity on the other end of the relationship (kind=edge)."
    },
    "node_type": {
      "type": "string",
      "enum": ["character", "npc", "location", "faction", "item", "plot_thread", "note"],
      "description": "The type of the new entry (kind=node)."
    },
    "name": {
      "type": "string",
      "description": "The name of the new entry (kind=node)."
    },
    "body": {
      "type": "string",
      "description": "The prose describing the new entry (kind=node)."
    }
  },
  "required": ["kind"]
}`)

// rememberArgs is the decoded LLM argument set. Scope is absent by design.
type rememberArgs struct {
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Fact     string `json:"fact"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
	NodeType string `json:"node_type"`
	Name     string `json:"name"`
	Body     string `json:"body"`
}

// RememberKnowledge is the first side-effecting built-in (#300, ADR-0052): an
// Agent's proposal that a new fact, relationship, or entry be added to the
// Knowledge Graph. Its ONLY effect is a pending Knowledge Proposal row for the
// GM to review — nothing touches campaign canon until the GM approves. It is
// therefore [ProposalMediated] and runs inline in the loop despite ReadOnly
// being false.
//
// The write authority is narrowed per Agent Role via the ADR-0029 grant scope,
// enforced HERE, never by the LLM:
//
//   - own_node (a Character NPC's default, and the fail-closed default): may
//     propose facts on its OWN linked Node and Edges FROM it. The subject and
//     anchor are the caller's own Node (resolved from the turn ctx, not the
//     args), so a crafted subject cannot make an innkeeper propose facts about
//     the distant war. Creating a new entry (kind=node) is refused.
//   - campaign (the Butler's grant): may propose facts, edges, and brand-new
//     entries anywhere in the Campaign; the subject is taken from the args.
type RememberKnowledge struct {
	dst KGWriter
}

// NewRememberKnowledge builds the Tool over dst. A nil dst registers the Tool
// (the grant editor's catalog is identical in every mode) but its Execute reports
// it is unavailable rather than panicking.
func NewRememberKnowledge(dst KGWriter) *RememberKnowledge {
	return &RememberKnowledge{dst: dst}
}

// Name implements [Tool].
func (*RememberKnowledge) Name() string { return "remember_knowledge" }

// Description implements [Tool]. It is hardened against the #411 proposal flood:
// the model is told to remember only genuinely NEW facts and never to re-remember
// something it already proposed this session (the tool echoes the target's pending
// proposals back on every call, and silently suppresses exact/normalized repeats).
func (*RememberKnowledge) Description() string {
	return "Remember a new fact, relationship or entry about the world. " +
		"It is saved as a suggestion for the GM to review — not instantly canon. " +
		"Only remember facts that are NOT already known to you: never re-propose a " +
		"fact you already remembered earlier in this session, and do not restate a " +
		"fact the world already records. Exact and normalized (case/punctuation) " +
		"repeats are dropped automatically, but a fact you reword in DIFFERENT words " +
		"is not caught — so do not re-propose the same fact phrased differently."
}

// InputSchema implements [Tool].
func (*RememberKnowledge) InputSchema() json.RawMessage { return rememberKnowledgeInputSchema }

// ReadOnly implements [Tool]: remember_knowledge writes a proposal, so it is not
// read-only (ADR-0030). It runs inline anyway via [ProposalMediated] (ADR-0052).
func (*RememberKnowledge) ReadOnly() bool { return false }

// ProposalMediated implements [ProposalMediated]: the only effect is a GM-reviewed
// proposal, so the loop runs it inline despite ReadOnly=false (ADR-0052).
func (*RememberKnowledge) ProposalMediated() bool { return true }

// SupportsScope implements [Tool]: the write authority is narrowed per Agent via
// the grant scope (own_node vs campaign), so the grant editor renders its scope
// UI (ADR-0029).
func (*RememberKnowledge) SupportsScope() bool { return true }

// Execute implements [Tool]. It resolves the effective scope from grantConfig
// (never the args, fail-closed to own_node), validates the per-kind arguments,
// builds the [ProposedWrite], and records it as a pending proposal. A nil writer
// reports unavailable; a misconfigured grant fails loudly; a bad argument or a
// scope violation returns an error result the LLM can read, and — for own_node
// refusals and unlinked callers — the writer is NEVER called.
func (rk *RememberKnowledge) Execute(ctx context.Context, args json.RawMessage, grantConfig any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if rk.dst == nil {
		return "", fmt.Errorf("remember_knowledge: knowledge writes are unavailable in this mode")
	}

	scope, err := parseScope(grantConfig)
	if err != nil {
		return "", fmt.Errorf("remember_knowledge: %w", err)
	}
	if scope == "" {
		scope = scopeOwnNode // write direction fails CLOSED to the narrowest scope (S3)
	}

	var a rememberArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("remember_knowledge: invalid arguments: %w", err)
	}
	a.Kind = strings.TrimSpace(a.Kind)

	if err := validateArgs(a); err != nil {
		return "", err
	}

	var w ProposedWrite
	switch scope {
	case scopeOwnNode:
		w, err = rk.ownNodeWrite(ctx, a)
	case scopeCampaign:
		w, err = campaignWrite(a)
	default:
		return "", fmt.Errorf("remember_knowledge: unsupported scope %q", scope)
	}
	if err != nil {
		return "", err
	}

	// Write-time dedup (#411, ADR-0052 mechanism a): suppress an exact/normalized
	// re-proposal of something already pending for this target or already canon on
	// its Node, and feed the agent its own pending proposals so it stops repeating
	// itself. The read is best-effort — a KG hiccup must never drop the NPC's
	// memory — so it FAILS OPEN to creating the row.
	var known KnownForTarget
	if k, err := rk.dst.ExistingKnowledge(ctx, CallerID(ctx), w); err == nil {
		known = k
		if matched, dup := firstKnownMatch(ProposalSalient(w), append(known.Established, known.Pending...)); dup {
			return alreadyNotedResult(matched, known.Pending), nil
		}
	} else {
		// Fail OPEN: a KG read hiccup must never drop the NPC's memory. Warn so a
		// SYSTEMATIC dedup failure (which silently reopens the proposal flood) is
		// visible to the operator instead of degrading unnoticed.
		slog.WarnContext(ctx, "remember_knowledge: dedup read failed; proposing without dedup", "error", err)
	}

	if err := rk.dst.CreateProposal(ctx, CallerID(ctx), w); err != nil {
		return "", fmt.Errorf("remember_knowledge: %w", err)
	}
	return notedResult(known.Pending), nil
}

// maxEchoedPending caps how many of the target's pending proposals are echoed
// back to the model, and maxEchoedRunes caps each one's length, so a busy target
// with long facts cannot blow the hot-context budget up via the echo (a count cap
// alone still admits ~20k characters).
const (
	maxEchoedPending = 10
	maxEchoedRunes   = 200
)

// alreadyNotedResult is the tool result when a proposal duplicates a known fact:
// no row was created, and it names the exact known wording (plus the target's
// other pending proposals) so the model stops re-remembering it.
func alreadyNotedResult(matched string, pending []string) string {
	return withPendingEcho(fmt.Sprintf(
		"Already noted — %q is already known, so no new suggestion was saved.", matched), pending)
}

// notedResult is the tool result for a freshly created proposal; it echoes the
// target's pending proposals so the model can see what it has already suggested
// this session (ADR-0052 mechanism c).
func notedResult(pending []string) string {
	return withPendingEcho("Noted — saved for the GM's review.", pending)
}

// withPendingEcho appends the target's pending proposal texts (capped) to a tool
// result, so the agent sees its own session proposals and does not repeat them.
func withPendingEcho(lead string, pending []string) string {
	if len(pending) == 0 {
		return lead
	}
	if len(pending) > maxEchoedPending {
		pending = pending[:maxEchoedPending]
	}
	quoted := make([]string, len(pending))
	for i, p := range pending {
		quoted[i] = fmt.Sprintf("%q", truncateRunes(p, maxEchoedRunes))
	}
	return lead + " You have already proposed these facts about this subject (do not repeat them): " +
		strings.Join(quoted, "; ") + "."
}

// validateArgs enforces the per-kind required fields and the text-length caps,
// BEFORE any scope resolution reaches the writer. It is intentionally scope-blind
// (subject requirements are checked per scope): an unknown kind or an overlong
// body must be rejected the same way for a Butler and an NPC.
func validateArgs(a rememberArgs) error {
	switch a.Kind {
	case proposalKindFact:
		if strings.TrimSpace(a.Fact) == "" {
			return fmt.Errorf("remember_knowledge: a fact requires the 'fact' text")
		}
		if err := capText("fact", a.Fact); err != nil {
			return err
		}
		if err := capName("subject", a.Subject); err != nil {
			return err
		}
	case proposalKindEdge:
		if !relationValues[a.Relation] {
			return fmt.Errorf("remember_knowledge: %q is not a known relation", a.Relation)
		}
		if strings.TrimSpace(a.Target) == "" {
			return fmt.Errorf("remember_knowledge: an edge requires a 'target'")
		}
		if err := capName("subject", a.Subject); err != nil {
			return err
		}
		if err := capName("target", a.Target); err != nil {
			return err
		}
	case proposalKindNode:
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("remember_knowledge: a new entry requires a 'name'")
		}
		if !nodeTypeValues[a.NodeType] {
			return fmt.Errorf("remember_knowledge: %q is not a known node_type", a.NodeType)
		}
		if err := capText("name", a.Name); err != nil {
			return err
		}
		if err := capText("body", a.Body); err != nil {
			return err
		}
	default:
		return fmt.Errorf("remember_knowledge: unknown kind %q", a.Kind)
	}
	return nil
}

// capText rejects a prose field longer than MaxProposalTextRunes (rune-counted).
func capText(field, s string) error {
	if utf8.RuneCountInString(s) > MaxProposalTextRunes {
		return fmt.Errorf("remember_knowledge: %s is too long (max %d characters)", field, MaxProposalTextRunes)
	}
	return nil
}

// capName rejects an entity-name field (subject/target) longer than
// MaxKGNameRunes (rune-counted). It is scope-blind so the cap holds for an
// own_node edge target as well as a campaign subject/target — no field escapes
// unbounded into the proposed_write jsonb.
func capName(field, s string) error {
	if utf8.RuneCountInString(s) > MaxKGNameRunes {
		return fmt.Errorf("remember_knowledge: %s is too long (max %d characters)", field, MaxKGNameRunes)
	}
	return nil
}

// ownNodeWrite builds a proposal anchored on the CALLER's own linked Node. A new
// entry (kind=node) is refused (an NPC may not create entries); the subject and
// anchor node_id are the caller's own Node, overwriting whatever the LLM
// supplied, and an Agent with no linked entry is refused with the writer never
// called.
func (rk *RememberKnowledge) ownNodeWrite(ctx context.Context, a rememberArgs) (ProposedWrite, error) {
	if a.Kind == proposalKindNode {
		return ProposedWrite{}, fmt.Errorf("remember_knowledge: you may not create new entries; you can only remember facts about yourself")
	}
	ref, ok, err := rk.dst.OwnNode(ctx, CallerID(ctx))
	if err != nil {
		return ProposedWrite{}, fmt.Errorf("remember_knowledge: %w", err)
	}
	if !ok {
		return ProposedWrite{}, fmt.Errorf("remember_knowledge: you have no linked wiki entry to remember facts about")
	}
	w := ProposedWrite{V: proposalWriteVersion, Kind: a.Kind, NodeID: ref.ID, Subject: ref.Name}
	switch a.Kind {
	case proposalKindFact:
		w.Fact = a.Fact
	case proposalKindEdge:
		w.Relation = a.Relation
		w.Target = a.Target
	}
	return w, nil
}

// campaignWrite builds a campaign-scoped proposal (the Butler's grant): all three
// kinds are allowed, the subject is preserved from the args, and there is no
// anchor node_id (the review surface resolves the subject by name).
func campaignWrite(a rememberArgs) (ProposedWrite, error) {
	w := ProposedWrite{V: proposalWriteVersion, Kind: a.Kind}
	switch a.Kind {
	case proposalKindFact:
		if strings.TrimSpace(a.Subject) == "" {
			return ProposedWrite{}, fmt.Errorf("remember_knowledge: a fact requires a 'subject'")
		}
		w.Subject = a.Subject
		w.Fact = a.Fact
	case proposalKindEdge:
		if strings.TrimSpace(a.Subject) == "" {
			return ProposedWrite{}, fmt.Errorf("remember_knowledge: an edge requires a 'subject'")
		}
		w.Subject = a.Subject
		w.Relation = a.Relation
		w.Target = a.Target
	case proposalKindNode:
		w.NodeType = a.NodeType
		w.Name = a.Name
		w.Body = a.Body
	}
	return w, nil
}

// Compile-time assertion that RememberKnowledge is proposal-mediated.
var _ ProposalMediated = (*RememberKnowledge)(nil)
