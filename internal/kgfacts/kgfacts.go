// Package kgfacts is the NPC Knowledge Graph fact-recall component (#126,
// ADR-0008 v1.0 / v1.5): the production [agent.FactsRecaller] the voice loop
// consults each turn to fill the reserved Hot Context KG-facts slot.
//
// It mirrors internal/recall's shape but is deliberately simpler — INLINE only,
// no speculation, no goroutine, no bus, no Close. The read is one indexed OLTP
// query (AgentNodeFacts — the Agent's own Node plus its single-hop neighbours),
// sub-millisecond, so speculation buys nothing (ADR-0042): a per-turn read means a
// gm_private flip or a new Edge mid-session takes effect on the very next turn,
// with no cache to invalidate.
//
// One contract, shared with [agent.MemoryRecaller]: Facts NEVER errors and NEVER
// stalls the turn. A slow/unavailable DB path, or the hard budget elapsing,
// degrades to nil (counted "degraded"). A barge cancels the turn ctx, which
// yields nil WITHOUT counting — the turn is gone, nothing was wasted. With no
// active session there is nothing to scope, so it yields nil ("empty"). A nil
// recaller is never constructed by the loop, so the turn behaves exactly as
// before (#126 byte-identical guarantee).
package kgfacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

const (
	// DefaultBudget is the hard inline-read budget inside the turn ctx: the public
	// Node read must finish within it or facts degrade to nil. An indexed OLTP read
	// is sub-ms, so this is a generous ceiling that only fires on a wedged DB.
	DefaultBudget = 50 * time.Millisecond
	// MaxFacts caps how many Node facts are injected in one turn.
	MaxFacts = 20
	// MaxFactChars caps one fact's body length in runes; a longer body is
	// rune-safe-truncated with a trailing ellipsis.
	MaxFactChars = 500
	// MaxNameChars caps a fact's Node-name length in runes. Without it a single
	// pathological name could push one fact past MaxBlockChars — and since the read
	// is newest-first, that oversized fact would land first and the deterministic
	// prefix-stop would drop the ENTIRE block (review finding, PR #202).
	MaxNameChars = 200
	// MaxBlockChars bounds the whole assembled facts block (header + joins + facts)
	// so the prompt budget holds regardless of wiki size (#126 AC4).
	MaxBlockChars = 4000
)

// factsHeader is the block header the agent's factsBlock prepends. kgfacts does
// NOT emit it — the agent is the joiner — but the block-budget accounting reserves
// its length so MaxBlockChars bounds the FINAL rendered block, header included.
const factsHeader = "## What you know about the world"

// blockJoin is the separator between the header and each fact (and between facts)
// in the agent's rendered block. Reserved in the block-budget accounting.
const blockJoin = "\n\n"

// PromptKG is the prompt-facing KG read seam kgfacts needs (#450): the Agent's
// edge-aware Node neighbourhood — its own linked Node plus its single-hop
// neighbours (both edge directions), gm-public only, bounded (the
// prompt-injection read, #133). It deliberately declares NO unfiltered KG read:
// what this interface returns lands in an NPC's Hot Context, so gm_private
// filtering is enforced by the seam (see the gm_private seam test in
// internal/storage), not by call-site discipline. storage's PromptKG() view
// satisfies it.
type PromptKG interface {
	AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error)
}

// Compile-time assertion: the storage prompt view satisfies the seam.
var _ PromptKG = storage.PromptKGView{}

// Metrics records KG-fact-read outcomes (#126, ADR-0032). *observe.PrometheusRecorder
// satisfies it; a nil Metrics is replaced with a no-op so call sites never check.
type Metrics interface {
	KGFacts(observe.FactsOutcome)
}

// Config tunes the recaller. Zero values take the package defaults.
type Config struct {
	// Budget is the hard inline-read budget inside the turn ctx. Default DefaultBudget.
	Budget time.Duration
}

func (c Config) withDefaults() Config {
	if c.Budget <= 0 {
		c.Budget = DefaultBudget
	}
	return c
}

// Recaller is the production [agent.FactsRecaller]. It holds no goroutine and no
// subscription (unlike recall) — the read is inline per turn. Safe for concurrent
// use (its deps are).
type Recaller struct {
	nodes   PromptKG
	metrics Metrics
	log     *slog.Logger
	budget  time.Duration
}

// New builds a Recaller wired to the prompt-facing KG read seam (pass storage's
// PromptKG() view) and the metrics sink. The active-session guard is now the run
// context's [session.Identity] (#487), so there is no session seam. Unlike recall
// it starts nothing and owns no resources to release.
func New(nodes PromptKG, metrics Metrics, log *slog.Logger, cfg Config) *Recaller {
	cfg = cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = discardMetrics{}
	}
	return &Recaller{
		nodes:   nodes,
		metrics: metrics,
		log:     log,
		budget:  cfg.Budget,
	}
}

// Facts implements [agent.FactsRecaller]. It returns the NPC's edge-aware Node
// facts this turn — the Agent's own linked Node plus its single-hop neighbours
// (#133, ADR-0008 amendment) — bounded and rendered, honoring the hard budget and
// degrading to nil rather than stalling. The read is keyed by the Agent, so an
// UNLINKED Character NPC gets an empty slot (no campaign-wide fallback); an
// unparseable/empty agentID yields nil, counted empty. It never errors or panics.
func (r *Recaller) Facts(ctx context.Context, agentID string) []string {
	if _, ok := session.FromContext(ctx); !ok {
		// No session Identity on the run context (defensive — Facts runs during a live
		// turn descended from the Manager's run context): nothing to scope, inject
		// nothing. Count it as an empty read, not a degradation (#487).
		r.metrics.KGFacts(observe.FactsEmpty)
		return nil
	}

	aid, err := uuid.Parse(agentID)
	if err != nil || aid == uuid.Nil {
		// A Persona with no persisted uuid: there is nothing to scope the
		// neighbourhood read to, so inject nothing. Empty, not degraded.
		r.metrics.KGFacts(observe.FactsEmpty)
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		// The turn ctx was already cancelled (a barge before the read even started):
		// yield nothing and count nothing.
		return r.degrade(ctx, err)
	}

	nodes, err := r.nodes.AgentNodeFacts(ctx, aid)
	if err != nil {
		return r.degrade(ctx, fmt.Errorf("agent node facts: %w", err))
	}

	facts := renderFacts(nodes)
	if len(facts) == 0 {
		r.metrics.KGFacts(observe.FactsEmpty)
		return nil
	}
	r.metrics.KGFacts(observe.FactsOK)
	return facts
}

// degrade yields nil. A cancelled ctx is a barge (ADR-0042): silent, NOT counted —
// the turn is gone, nothing was wasted. Any other failure (budget elapsed, DB
// error) logs and counts a degraded read.
func (r *Recaller) degrade(ctx context.Context, cause error) []string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	r.log.Warn("kg facts degraded to no-facts", "err", cause)
	r.metrics.KGFacts(observe.FactsDegraded)
	return nil
}

// renderFacts projects the public Nodes (in storage order) into rendered fact
// strings, applying the per-fact truncation and the MaxFacts / MaxBlockChars caps.
// The block-budget accounting reserves the agent's header + joins so the FINAL
// rendered block (header included) stays within MaxBlockChars. It stops at the
// first fact that would overrun either cap — a deterministic prefix, never a
// skip-scan — so a huge Node cannot let a later small one sneak in past the budget.
func renderFacts(nodes []storage.KGNode) []string {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(nodes))
	// Every fact in the assembled block is preceded by a blockJoin (the first by the
	// header's join, the rest by the inter-fact join), so the running total starts at
	// the header length and each fact adds len(join)+len(fact).
	total := len(factsHeader)
	for _, n := range nodes {
		if len(out) >= MaxFacts {
			break
		}
		fact := renderFact(n)
		delta := len(blockJoin) + len(fact)
		if total+delta > MaxBlockChars {
			break
		}
		total += delta
		out = append(out, fact)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renderFact renders one Node as "### <Name> (<TypeLabel>)\n<Body>". The name is
// rune-safe-truncated to MaxNameChars and the body to MaxFactChars, each with a
// trailing ellipsis when cut, so no single Node can blow the block budget. A
// bodiless Node emits only its header line (no dangling newline).
func renderFact(n storage.KGNode) string {
	head := fmt.Sprintf("### %s (%s)", truncateRunes(n.Name, MaxNameChars), typeLabel(n.Type))
	body := truncateRunes(strings.TrimSpace(n.Body), MaxFactChars)
	if body == "" {
		return head
	}
	return head + "\n" + body
}

// typeLabel maps a Node type onto its GM-facing label (#126 test contract) via
// the single label map in pkg/kgvocab (#449); an unknown type falls back to
// "Note" there.
func typeLabel(t storage.KGNodeType) string {
	return kgvocab.NodeTypeLabel(string(t))
}

// truncateRunes trims s to at most max runes, appending an ellipsis when it cut —
// rune-safe so a multibyte character is never split mid-codepoint.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// discardMetrics is the no-op Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) KGFacts(observe.FactsOutcome) {}

// Static assertion that Recaller is a FactsRecaller.
var _ agent.FactsRecaller = (*Recaller)(nil)
