package billing

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// FlushFunc persists accumulated ledger rows; [storage.Store.AddUsage] satisfies
// it. Rows commute (upsert-accumulate), so callers only guarantee each buffered
// batch is sent once.
type FlushFunc func(ctx context.Context, rows []storage.UsageRow) error

// Ledger is the durable-usage counterpart of the in-memory spend meter
// (ADR-0054): an [observe.UsageSink] that buckets a session's metered usage by
// (day, component, provider, model) with a priced estimate, for the session
// Manager to flush into the usage_ledger table at session end. Like the meter
// it rides [observe.TeeUsage] beside the production recorder — zero new
// pipeline plumbing — and it never gates anything: attribution only.
//
// Buffering is in-memory until Flush: a crash loses the unflushed remainder of
// the session, which the ADR accepts for an estimates-only ledger (the
// Prometheus counters still moved).
type Ledger struct {
	tenantID uuid.UUID
	now      func() time.Time

	mu   sync.Mutex
	rows map[ledgerKey]*storage.UsageRow
}

type ledgerKey struct {
	day       string // yyyy-mm-dd (UTC)
	component storage.Component
	provider  observe.Provider
	model     string
}

// NewLedger builds a ledger for one Tenant's session. now is injectable for
// tests; nil uses time.Now.
func NewLedger(tenantID uuid.UUID, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{
		tenantID: tenantID,
		now:      now,
		rows:     map[ledgerKey]*storage.UsageRow{},
	}
}

// LLMTokens accumulates one completion's tokens (implements
// [observe.UsageSink]). Image generation rides this sink too (Gemini meters a
// generated image as output tokens, ADR-0045), so its usage lands under the llm
// component with the model distinguishing it.
func (l *Ledger) LLMTokens(provider observe.Provider, model string, inputTokens, outputTokens int) {
	row := func(r *storage.UsageRow) {
		r.LLMInputTokens += int64(inputTokens)
		r.LLMOutputTokens += int64(outputTokens)
		r.EstimatedUSD += spend.EstimateLLMUSD(provider, model, inputTokens, outputTokens)
	}
	l.accumulate(storage.ComponentLLM, provider, model, row)
}

// TTSCharacters accumulates one synthesis's characters (implements
// [observe.UsageSink]).
func (l *Ledger) TTSCharacters(provider observe.Provider, chars int) {
	l.accumulate(storage.ComponentTTS, provider, "", func(r *storage.UsageRow) {
		r.TTSCharacters += int64(chars)
		r.EstimatedUSD += spend.EstimateTTSUSD(provider, chars)
	})
}

// STTAudioSeconds accumulates one recognition's audio duration (implements
// [observe.UsageSink]).
func (l *Ledger) STTAudioSeconds(provider observe.Provider, d time.Duration) {
	l.accumulate(storage.ComponentSTT, provider, "", func(r *storage.UsageRow) {
		r.STTAudioSeconds += d.Seconds()
		r.EstimatedUSD += spend.EstimateSTTUSD(provider, d)
	})
}

// accumulate applies fold to the bucket for (today, component, provider, model),
// creating it on first use.
func (l *Ledger) accumulate(component storage.Component, provider observe.Provider, model string, fold func(*storage.UsageRow)) {
	day := l.now().UTC().Truncate(24 * time.Hour)
	key := ledgerKey{day: day.Format(time.DateOnly), component: component, provider: provider, model: model}

	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.rows[key]
	if !ok {
		r = &storage.UsageRow{
			TenantID:  l.tenantID,
			Day:       day,
			Component: component,
			Provider:  string(provider),
			Model:     model,
		}
		l.rows[key] = r
	}
	fold(r)
}

// Flush drains the buffered buckets through flush. The buffer is snapshotted
// and cleared BEFORE calling flush (usage arriving during a flush lands in the
// next batch); on error the snapshot is merged back so a later Flush retries —
// rows commute, so re-merging never mis-counts.
func (l *Ledger) Flush(ctx context.Context, flush FlushFunc) error {
	l.mu.Lock()
	if len(l.rows) == 0 {
		l.mu.Unlock()
		return nil
	}
	snapshot := l.rows
	l.rows = map[ledgerKey]*storage.UsageRow{}
	l.mu.Unlock()

	rows := make([]storage.UsageRow, 0, len(snapshot))
	for _, r := range snapshot {
		rows = append(rows, *r)
	}
	if err := flush(ctx, rows); err != nil {
		// Merge the failed snapshot back under the CURRENT buffer (which may have
		// gained rows meanwhile): quantities and estimates are additive.
		l.mu.Lock()
		for k, r := range snapshot {
			if cur, ok := l.rows[k]; ok {
				cur.LLMInputTokens += r.LLMInputTokens
				cur.LLMOutputTokens += r.LLMOutputTokens
				cur.TTSCharacters += r.TTSCharacters
				cur.STTAudioSeconds += r.STTAudioSeconds
				cur.EstimatedUSD += r.EstimatedUSD
			} else {
				l.rows[k] = r
			}
		}
		l.mu.Unlock()
		return err
	}
	return nil
}

var _ observe.UsageSink = (*Ledger)(nil)
