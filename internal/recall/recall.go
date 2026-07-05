// Package recall is the NPC memory-recall component (#122, ADR-0011/0042): the
// production [agent.MemoryRecaller] the voice loop consults each turn to fill the
// Hot Context memory slot.
//
// Two paths, one contract (recall NEVER stalls the turn and NEVER errors):
//
//   - Speculative: during speech, the recaller subscribes to [voiceevent.STTPartial]
//     and, off the turn path, embeds the stabilized interim text and prefetches the
//     world-context ANN chunks (ADR-0011's campaign-scoped mode). At [Recaller.Recall]
//     time, if the final utterance matches the speculated query (normalized), the
//     prefetched vector and world chunks are reused and only the NPC-knowledge ANN
//     (target-agent-scoped, unknown during speech) runs — a single indexed query — so
//     memory injects at effectively zero added latency (a "hit").
//   - Inline: no usable speculation (mismatch, no partials, batch STT) falls back to
//     embedding the utterance and running BOTH ANN modes within a hard budget
//     (~250ms, ADR-0042) inside the turn ctx (a "miss").
//
// A slow or unavailable embeddings/DB path, or the budget elapsing, degrades to a
// zero [agent.Memory] ("skip", counted). A barge cancels the turn ctx, which
// cancels retrieval and yields zero Memory WITHOUT counting a skip — the turn is
// gone, nothing was wasted (ADR-0042). With no configured recaller the agent loop
// never constructs one, so the turn behaves exactly as before (AC6).
package recall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Retriever is the ANN retrieval surface the recaller needs (#119, ADR-0011):
// NPC-knowledge (participated-agent filter) and world-context (campaign only).
// *storage.Store satisfies it.
type Retriever interface {
	SearchChunksByAgent(ctx context.Context, campaignID, agentID uuid.UUID, query []float32, k int) ([]storage.ChunkMatch, error)
	SearchChunksByCampaign(ctx context.Context, campaignID uuid.UUID, query []float32, k int) ([]storage.ChunkMatch, error)
}

// Sessions is the narrow read the recaller needs from the SessionManager: which
// Voice Session (hence which Campaign) is active, so retrieval is campaign-scoped.
// *session.Manager satisfies it via Snapshot (the same shape the transcript relay
// depends on); defined locally so recall does not import session.
type Sessions interface {
	Snapshot() (storage.VoiceSession, bool)
}

// Metrics records recall outcomes (#122, ADR-0032). *observe.PrometheusRecorder
// satisfies it; a nil Metrics is replaced with a no-op so call sites never check.
type Metrics interface {
	MemoryRecall(observe.RecallOutcome)
}

// Config tunes the recaller. Zero values take the package defaults.
type Config struct {
	// Budget is the hard inline-retrieval budget inside the turn ctx (ADR-0042):
	// embed + both ANN searches must finish within it or recall degrades to
	// no-memory. Default 250ms.
	Budget time.Duration
	// K is the number of chunks fetched per retrieval mode. Default 3.
	K int
}

const (
	defaultBudget = 250 * time.Millisecond
	defaultK      = 3

	// minSpeculateWords is the shortest normalized partial worth embedding: a one-
	// or two-word interim ("do you") carries no retrieval signal and would only
	// churn the provider.
	minSpeculateWords = 3
	// minEmbedInterval rate-limits speculative embeds so a fast partial stream does
	// not fire an embed per interim; the latest text is picked up on the next tick.
	minEmbedInterval = 200 * time.Millisecond
	// speculateTimeout bounds one off-path speculative embed+prefetch so a wedged
	// provider cannot pin the single speculator goroutine forever (it just misses
	// the chance to help; the inline path self-heals at the final).
	speculateTimeout = 5 * time.Second
)

func (c Config) withDefaults() Config {
	if c.Budget <= 0 {
		c.Budget = defaultBudget
	}
	if c.K <= 0 {
		c.K = defaultK
	}
	return c
}

// Recaller is the production [agent.MemoryRecaller]. Construct with [New]; it
// subscribes to the bus and starts one speculator goroutine at construction, both
// released by [Recaller.Close]. Safe for concurrent use.
type Recaller struct {
	embedder  embeddings.Provider
	retriever Retriever
	sessions  Sessions
	metrics   Metrics
	log       *slog.Logger
	budget    time.Duration
	k         int

	now func() time.Time // injected in tests; time.Now in production

	// speculation state (see speculate.go)
	ctx         context.Context
	cancel      context.CancelFunc
	unsubscribe func()
	signal      chan struct{} // 1-slot wake for the speculator
	specDone    chan struct{} // closed when the speculator goroutine exits
	speculated  chan struct{} // 1-slot test notify: a speculation pass completed

	mailMu     sync.Mutex
	pending    string
	hasPending bool

	// lastEmbed* gate the speculator; touched only by the speculator goroutine.
	lastEmbedNorm string
	lastEmbedAt   time.Time

	cacheMu sync.Mutex
	cache   specCache
}

// New builds a Recaller wired to the embeddings provider, the ANN retriever, the
// session source (for the active Campaign), the process bus (for STTPartial
// speculation), and the metrics sink. It subscribes to the bus and launches the
// speculator goroutine immediately; call [Recaller.Close] on shutdown to release
// both.
func New(embedder embeddings.Provider, retriever Retriever, sessions Sessions, bus *voiceevent.Bus, metrics Metrics, log *slog.Logger, cfg Config) *Recaller {
	cfg = cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = discardMetrics{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Recaller{
		embedder:   embedder,
		retriever:  retriever,
		sessions:   sessions,
		metrics:    metrics,
		log:        log,
		budget:     cfg.Budget,
		k:          cfg.K,
		now:        time.Now,
		ctx:        ctx,
		cancel:     cancel,
		signal:     make(chan struct{}, 1),
		specDone:   make(chan struct{}),
		speculated: make(chan struct{}, 1),
	}
	r.unsubscribe = voiceevent.On(bus, r.onPartial)
	go r.speculateLoop()
	return r
}

// Close unsubscribes from the bus and stops the speculator goroutine. It is
// idempotent-safe only once (a second call blocks on the closed done channel);
// wire it once, on process shutdown. Tests defer it.
func (r *Recaller) Close() {
	r.unsubscribe()
	r.cancel()
	<-r.specDone
}

// Recall implements [agent.MemoryRecaller]. It returns the Hot Context memory for
// this turn, honoring the bounded-sync budget and degrading to a zero Memory
// rather than stalling. It never returns an error and never panics.
func (r *Recaller) Recall(ctx context.Context, agentID, utterance string) agent.Memory {
	aid, err := uuid.Parse(agentID)
	if err != nil {
		// Defensive, consistent with the chunker's posture: an unparseable agent id
		// is a wiring bug, not a turn failure — skip and move on.
		r.log.Warn("memory recall: unparseable agent id; skipping", "agent_id", agentID, "err", err)
		r.metrics.MemoryRecall(observe.RecallSkip)
		return agent.Memory{}
	}
	campaignID, ok := r.campaign()
	if !ok {
		// No active session to scope retrieval — recall runs during a live turn, so
		// this is defensive; count it as a skip.
		r.metrics.MemoryRecall(observe.RecallSkip)
		return agent.Memory{}
	}

	ctx, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		// The turn ctx was already cancelled (a barge before recall even started):
		// yield nothing and count nothing.
		return r.degrade(ctx, err)
	}

	norm := normalize(utterance)
	vec, world, hit := r.cacheLookup(norm)

	if !hit {
		vecs, err := r.embedder.Embed(ctx, []string{utterance})
		if err != nil {
			return r.degrade(ctx, fmt.Errorf("embed utterance: %w", err))
		}
		if len(vecs) != 1 {
			return r.degrade(ctx, fmt.Errorf("embed returned %d vectors, want 1", len(vecs)))
		}
		vec = vecs[0]
		w, err := r.retriever.SearchChunksByCampaign(ctx, campaignID, vec, r.k)
		if err != nil {
			return r.degrade(ctx, fmt.Errorf("search world chunks: %w", err))
		}
		world = w
	}

	// NPC-knowledge is always run at Recall time: the target agent is unknown
	// during speech, so a hit still owes this one indexed, sub-ms query.
	personal, err := r.retriever.SearchChunksByAgent(ctx, campaignID, aid, vec, r.k)
	if err != nil {
		return r.degrade(ctx, fmt.Errorf("search agent chunks: %w", err))
	}

	if hit {
		r.metrics.MemoryRecall(observe.RecallHit)
	} else {
		r.metrics.MemoryRecall(observe.RecallMiss)
	}
	return agent.Memory{
		Personal: chunkContents(personal),
		World:    chunkContents(world),
	}
}

// degrade yields a zero Memory. A cancelled ctx is a barge (ADR-0042): silent,
// NOT counted — the turn is gone, nothing was wasted. Any other failure (budget
// elapsed, provider/DB error) logs and counts a skip.
func (r *Recaller) degrade(ctx context.Context, cause error) agent.Memory {
	if errors.Is(ctx.Err(), context.Canceled) {
		return agent.Memory{}
	}
	r.log.Warn("memory recall degraded to no-memory", "err", cause)
	r.metrics.MemoryRecall(observe.RecallSkip)
	return agent.Memory{}
}

// campaign reads the active session's Campaign id, or false when idle.
func (r *Recaller) campaign() (uuid.UUID, bool) {
	vs, ok := r.sessions.Snapshot()
	if !ok {
		return uuid.Nil, false
	}
	return vs.CampaignID, true
}

// chunkContents projects ANN matches to their chunk contents in rank order
// (nearest first). Tolerant of fewer than k results (the HNSW post-filter can
// return fewer — normal, ADR-0011).
func chunkContents(matches []storage.ChunkMatch) []string {
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Chunk.Content)
	}
	return out
}

// discardMetrics is the no-op Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) MemoryRecall(observe.RecallOutcome) {}

// Static assertion that Recaller is a MemoryRecaller.
var _ agent.MemoryRecaller = (*Recaller)(nil)
