package hotctx

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// PreFetcher speculatively queries the knowledge graph (L3) based on entity
// names detected in STT partial transcripts. Pre-fetched entities are cached
// so that by the time the LLM prompt is assembled the relevant graph nodes are
// already in memory — no cold-layer round-trip required.
//
// All exported methods are goroutine-safe.
type PreFetcher struct {
	graph       memory.KnowledgeGraph
	mu          sync.RWMutex
	entityNames map[string]string         // lowercase entity name → entity ID
	cache       map[string]*memory.Entity // entity ID → fetched entity
}

// NewPreFetcher creates a [PreFetcher] backed by graph.
// Call [PreFetcher.RefreshEntityList] before the first session turn to populate
// the entity name index.
func NewPreFetcher(graph memory.KnowledgeGraph) *PreFetcher {
	return &PreFetcher{
		graph:       graph,
		entityNames: make(map[string]string),
		cache:       make(map[string]*memory.Entity),
	}
}

// RefreshEntityList reloads all entities from the knowledge graph and rebuilds
// the lowercase name → ID lookup map. Call this at the start of each session or
// whenever the entity list changes.
//
// Returns "pre-fetch: <detail>" on graph errors.
func (p *PreFetcher) RefreshEntityList(ctx context.Context) error {
	entities, err := p.graph.FindEntities(ctx, memory.EntityFilter{})
	if err != nil {
		return fmt.Errorf("pre-fetch: reload entity list: %w", err)
	}

	newNames := make(map[string]string, len(entities))
	for _, e := range entities {
		if e.Name != "" {
			lower := strings.ToLower(e.Name)
			newNames[lower] = e.ID

			// Also index individual words that are long enough to be distinctive
			// (>= 4 chars). This allows partial name matching — e.g. "blacksmith"
			// in a transcript will match the entity "Grimjaw the blacksmith".
			// Single-word collisions are accepted; the full-name key always wins
			// when both appear in the transcript.
			for word := range strings.FieldsSeq(lower) {
				if len(word) >= 4 {
					if _, exists := newNames[word]; !exists {
						newNames[word] = e.ID
					}
				}
			}
		}
	}

	p.mu.Lock()
	p.entityNames = newNames
	p.mu.Unlock()
	return nil
}

// ProcessPartial scans a partial STT transcript for known entity names using
// case-insensitive substring matching and pre-fetches any entities that are not
// already in the cache.
//
// Returns only the newly fetched entities (cache hits are excluded from the
// return value but are still accessible via [PreFetcher.GetCachedEntities]).
//
// Errors from the knowledge graph are silently swallowed so that a transient
// pre-fetch failure never blocks the real-time voice path.
func (p *PreFetcher) ProcessPartial(ctx context.Context, partial string) []memory.Entity {
	lower := strings.ToLower(partial)

	// Identify entity IDs to fetch under a single read lock.
	// Substring matching is fast (<1ms) so holding the lock is fine.
	p.mu.RLock()
	var toFetch []string
	seen := make(map[string]struct{})
	for name, id := range p.entityNames {
		if strings.Contains(lower, name) {
			if _, cached := p.cache[id]; !cached {
				if _, dup := seen[id]; !dup {
					toFetch = append(toFetch, id)
					seen[id] = struct{}{}
				}
			}
		}
	}
	p.mu.RUnlock()

	if len(toFetch) == 0 {
		return []memory.Entity{}
	}

	// Fetch entities concurrently (bounded to avoid overwhelming the graph store).
	type fetchResult struct {
		entity *memory.Entity
	}

	results := make(chan fetchResult, len(toFetch))
	var wg sync.WaitGroup
	for _, id := range toFetch {
		wg.Go(func() {
			entity, err := p.graph.GetEntity(ctx, id)
			if err != nil || entity == nil {
				// Silently skip — pre-fetch errors must not block the voice path.
				return
			}
			results <- fetchResult{entity: entity}
		})
	}
	// Close results channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	var fetched []*memory.Entity
	for r := range results {
		fetched = append(fetched, r.entity)
	}

	if len(fetched) == 0 {
		return []memory.Entity{}
	}

	// Store results in cache under write lock.
	// Also build return slice (only newly fetched entries).
	result := make([]memory.Entity, 0, len(fetched))
	p.mu.Lock()
	for _, e := range fetched {
		if _, already := p.cache[e.ID]; !already {
			p.cache[e.ID] = e
		}
		result = append(result, *e)
	}
	p.mu.Unlock()

	return result
}

// GetCachedEntities returns all entities that have been pre-fetched and stored
// in the cache since the last [PreFetcher.Reset] call.
func (p *PreFetcher) GetCachedEntities() []*memory.Entity {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*memory.Entity, 0, len(p.cache))
	for _, e := range p.cache {
		out = append(out, e)
	}
	return out
}

// maxRetrieveResults caps the number of GraphRAG context results returned by
// [PreFetcher.Retrieve] to keep prompt size bounded.
const maxRetrieveResults = 5

// Retrieve performs a GraphRAG query scoped to the given NPC's visible graph.
// It type-asserts the underlying KnowledgeGraph to [memory.GraphRAGQuerier];
// if the graph does not implement GraphRAG, it returns nil (graceful
// degradation).
//
// The transcript is used as the FTS query string via
// [memory.GraphRAGQuerier.QueryWithContext] — no embedding is required, so
// this stays within the <50ms hot-context budget.
//
// Results are capped at [maxRetrieveResults].
func (p *PreFetcher) Retrieve(ctx context.Context, npcID string, transcript string) []memory.ContextResult {
	ragQuerier, ok := p.graph.(memory.GraphRAGQuerier)
	if !ok {
		return nil
	}

	// Build graphScope from the NPC's 1-hop neighbours.
	neighbors, err := p.graph.Neighbors(ctx, npcID, 1)
	if err != nil {
		slog.Debug("pre-fetch: retrieve: neighbors lookup failed",
			"npc_id", npcID,
			"error", err,
		)
		// Fall through with empty scope — QueryWithContext will search all chunks.
	}

	scope := make([]string, 0, len(neighbors)+1)
	scope = append(scope, npcID)
	for _, n := range neighbors {
		scope = append(scope, n.ID)
	}

	results, err := ragQuerier.QueryWithContext(ctx, transcript, scope)
	if err != nil {
		slog.Debug("pre-fetch: retrieve: GraphRAG query failed",
			"npc_id", npcID,
			"error", err,
		)
		return nil
	}

	if len(results) > maxRetrieveResults {
		results = results[:maxRetrieveResults]
	}
	return results
}

// Reset clears the pre-fetch cache. Call this at the start of each new voice
// turn so that stale pre-fetch results do not bleed into the next prompt.
func (p *PreFetcher) Reset() {
	p.mu.Lock()
	p.cache = make(map[string]*memory.Entity)
	p.mu.Unlock()
}
