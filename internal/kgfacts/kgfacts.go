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

// HasContent reports whether a rendered fact says anything beyond its header
// line. A Node with no body and no public Aspects still renders as
// "### Bart (NPC)" — a real fact string, but zero world knowledge — so any check
// of the form "does this NPC have facts" must count CONTENT-BEARING facts or it
// can never fail (#544 review finding).
func HasContent(fact string) bool {
	_, content, ok := strings.Cut(fact, "\n")
	return ok && strings.TrimSpace(content) != ""
}

// Preview is exactly what an Agent's Hot Context facts block will contain, plus
// the budget arithmetic that produced it (#535). It exists so the GM-facing
// "what does this NPC actually know" lens reads the SAME renderer the voice loop
// injects, instead of reimplementing the caps in TypeScript where they would
// silently drift from reality on the next change to either side.
type Preview struct {
	// Facts is the rendered block, fact by fact — byte-identical to what the turn
	// would inject.
	Facts []string
	// IncludedIDs are the Nodes that made it into Facts, in order. The lens dims the
	// graph to exactly these.
	IncludedIDs []uuid.UUID
	// DroppedIDs are Nodes the read returned that the caps excluded — the visible
	// half of the deterministic prefix-stop, which is otherwise a silent quality
	// cliff.
	DroppedIDs []uuid.UUID
	// Chars is the assembled block's length, header and joins included; MaxChars is
	// the budget it is measured against.
	Chars    int
	MaxChars int
	// MaxFacts is the count cap.
	MaxFacts int
	// Truncated reports that the prefix-stop fired: at least one Node the read
	// returned did not fit.
	Truncated bool
	// ContentFacts is how many of Facts say anything beyond their header line.
	// It, not len(Facts), is the honest answer to "does this NPC know anything":
	// a linked NPC's own Node always renders, empty or not.
	ContentFacts int
}

// RenderPreview is renderFacts with its budget arithmetic exposed (#535). The
// voice loop keeps calling renderFacts; both share this one implementation, so the
// preview cannot drift from what is actually injected.
func RenderPreview(nodes []storage.KGNode) Preview {
	p := Preview{MaxChars: MaxBlockChars, MaxFacts: MaxFacts}
	// Every fact in the assembled block is preceded by a blockJoin (the first by the
	// header's join, the rest by the inter-fact join), so the running total starts at
	// the header length and each fact adds len(join)+len(fact).
	total := len(factsHeader)
	// stopped makes the cut a deterministic PREFIX-stop rather than a skip-scan:
	// once one Node overruns a cap, every later Node is dropped too, so a huge Node
	// cannot let a smaller one behind it sneak in past the budget. The loop keeps
	// running only to RECORD what was dropped — which is the whole point of the
	// preview, since that cut is otherwise a silent quality cliff.
	stopped := false
	for _, n := range nodes {
		if !stopped {
			if len(p.Facts) >= MaxFacts {
				stopped = true
			} else {
				fact := renderFact(n)
				delta := len(blockJoin) + len(fact)
				if total+delta > MaxBlockChars {
					stopped = true
				} else {
					total += delta
					p.Facts = append(p.Facts, fact)
					p.IncludedIDs = append(p.IncludedIDs, n.ID)
				}
			}
		}
		if stopped {
			p.Truncated = true
			p.DroppedIDs = append(p.DroppedIDs, n.ID)
		}
	}
	if len(p.Facts) > 0 {
		p.Chars = total
	}
	for _, f := range p.Facts {
		if HasContent(f) {
			p.ContentFacts++
		}
	}
	return p
}

// renderFacts projects the public Nodes (in storage order) into rendered fact
// strings, applying the per-fact truncation and the MaxFacts / MaxBlockChars caps.
// The block-budget accounting reserves the agent's header + joins so the FINAL
// rendered block (header included) stays within MaxBlockChars.
func renderFacts(nodes []storage.KGNode) []string {
	if len(nodes) == 0 {
		return nil
	}
	return RenderPreview(nodes).Facts
}

// renderFact renders one Node as "### <Name> (<TypeLabel>)" followed by its
// content: the Node's public Aspects as "- <Key>: <Value>" lines (#542), then its
// free-form body remainder. The name is rune-safe-truncated to MaxNameChars and
// the COMPOSED content to MaxFactChars, each with a trailing ellipsis when cut, so
// one fact stays bounded at head + MaxFactChars no matter how many Aspects a Node
// carries — the budget arithmetic in renderFacts is unchanged, and a Node with no
// Aspects renders byte-identically to before this slice.
//
// Aspects are already public-only: the [PromptKG] seam filters gm_private rows in
// SQL. The defensive skip below costs nothing and keeps the guarantee readable at
// the point it matters.
func renderFact(n storage.KGNode) string {
	head := fmt.Sprintf("### %s (%s)", truncateRunes(n.Name, MaxNameChars), typeLabel(n.Type))
	// composeContent owns the whole per-fact bound. Truncating its result AGAIN
	// here would cut from the tail and undo the split — deleting the body it just
	// reserved room for.
	content := composeContent(n)
	if content == "" {
		return head
	}
	return head + "\n" + content
}

// MinBodyShare is the fraction of MaxFactChars the free-form body is guaranteed
// when a Node's Aspects and body together overrun the per-fact budget.
//
// Without a reserve, Aspects — which are composed FIRST — could consume the whole
// budget and evict the GM's authored prose entirely, silently and with no signal.
// A GM who writes both meant both to reach the table, so each side is cut rather
// than one deleting the other.
const MinBodyShare = 0.4

// composeContent joins a Node's public Aspect lines and its body remainder into
// the content string renderFact emits.
//
// When the two together exceed MaxFactChars, each is trimmed to its own share
// rather than the composite being cut from the end: cutting the tail would drop
// the body wholesale for any Node with 500 runes of Aspects. Whichever side needs
// less than its share donates the slack to the other, so a short body still leaves
// the Aspects nearly all of the budget.
//
// An Aspect with no value renders as its key alone rather than a dangling colon.
func composeContent(n storage.KGNode) string {
	var b strings.Builder
	for _, a := range n.Aspects {
		if a.GMPrivate {
			continue
		}
		key := strings.TrimSpace(a.Key)
		value := strings.TrimSpace(a.Value)
		if key == "" && value == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		switch {
		case key == "":
			b.WriteString(value)
		case value == "":
			b.WriteString(key)
		default:
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
		}
	}
	aspects := b.String()
	body := strings.TrimSpace(n.Body)

	// One source: the pre-aspect behaviour, unchanged.
	switch {
	case body == "":
		return truncateRunes(aspects, MaxFactChars)
	case aspects == "":
		return truncateRunes(body, MaxFactChars)
	}

	const sep = "\n\n"
	aspectRunes, bodyRunes := len([]rune(aspects)), len([]rune(body))
	if aspectRunes+bodyRunes+len(sep) <= MaxFactChars {
		return aspects + sep + body
	}

	// Over budget: split it. Each side is guaranteed a share, and whatever it does
	// not use goes to the other — so a one-line body costs the aspects almost
	// nothing, while a long one still cannot be deleted outright.
	budget := MaxFactChars - len(sep)
	bodyShare := int(float64(budget) * MinBodyShare)
	aspectShare := budget - bodyShare
	if bodyRunes < bodyShare {
		aspectShare += bodyShare - bodyRunes
		bodyShare = bodyRunes
	} else if aspectRunes < aspectShare {
		bodyShare += aspectShare - aspectRunes
		aspectShare = aspectRunes
	}
	return fitRunes(aspects, aspectShare) + sep + fitRunes(body, bodyShare)
}

// fitRunes trims s to at most max runes INCLUDING the ellipsis it adds when it
// cuts — unlike truncateRunes, whose result is max+1 when it cuts. The split above
// needs exactness: two parts that each overshoot by a rune would push the composed
// fact past the budget the split exists to respect.
func fitRunes(s string, max int) string {
	// A negative max would panic on the slice below. It is unreachable with the
	// current constants — every share is either >= MinBodyShare of a positive
	// budget or equal to an actual rune count — but this function is one tuning
	// change away from being called with one, and a panic in the prompt path takes
	// down a live turn.
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
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
