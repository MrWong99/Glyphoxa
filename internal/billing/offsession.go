package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// This file is THE seam for off-session paid provider calls (#658, ADR-0054
// amendment 2026-11-20): every model call made outside a live Voice Session —
// a recap, an assist draft, a generated map or portrait, a Highlight image or
// sound, a voice preview, a planning chat exchange — opens an [OffSession]
// call. Opening it is the only way to get a priced recorder off-session, and
// it always does three things together, so a new feature cannot do one and
// forget the others:
//
//  1. checks the Tenant's monthly plan allowance BEFORE anything is spent and
//     refuses with [ErrAllowanceExhausted];
//  2. tees a [Ledger] so the call's usage reaches usage_ledger, flushed when
//     the call closes — on failure paths too;
//  3. prices the usage on a caps-free meter for the call site's attribution
//     log line.
//
// The live Voice Session is NOT a client: session.Manager owns its own meter,
// hard cap and periodically flushed ledger (ADR-0046, ADR-0054).

// ErrAllowanceExhausted is returned when a Tenant's monthly plan allowance
// (ADR-0055 gate (b)) is spent: the paid call is refused BEFORE any provider
// request. The RPC tier maps it to one localizable refusal; a background job
// skips its paid step.
var ErrAllowanceExhausted = errors.New("billing: this month's included usage is exhausted")

// offSessionFlushTimeout bounds the ledger flush at the end of a call. The
// flush runs detached from the call's context: a client that hung up, or a
// request that timed out, still used what it used.
const offSessionFlushTimeout = 10 * time.Second

// AllowanceChecker is the ADR-0055 monthly allowance read;
// [spend.PlanAllowance] satisfies it.
type AllowanceChecker interface {
	AllowanceState(ctx context.Context, tenantID uuid.UUID) (spend.AllowanceState, error)
}

// UsageWriter persists ledger rows; *storage.Store satisfies it.
type UsageWriter interface {
	AddUsage(ctx context.Context, rows []storage.UsageRow) error
}

// OffSession opens metered off-session calls. Build ONE per process at the
// composition root and hand it to every engine that spends provider money
// outside a live session. Safe for concurrent use.
type OffSession struct {
	usage     UsageWriter
	allowance AllowanceChecker
	log       *slog.Logger
}

// NewOffSession builds the seam. usage receives every call's ledger rows and
// must not be nil (a wiring bug: it panics here, not at the first paid call).
// allowance is the plan-allowance gate: nil means no gate, which is the
// `allowlist` Admission Mode posture (self-hosts have no plans); `open` mode
// wires [spend.PlanAllowance]. A Tenant whose plan carries no allowance (every
// BYOK plan: it pays its own provider) is never refused either way, exactly
// like session start. log receives flush failures and the unknown-price
// warning (nil discards).
func NewOffSession(usage UsageWriter, allowance AllowanceChecker, log *slog.Logger) *OffSession {
	if usage == nil {
		panic("billing: NewOffSession: nil usage writer")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &OffSession{usage: usage, allowance: allowance, log: log}
}

// Check reports whether the Tenant may start a paid off-session call now:
// nil, or [ErrAllowanceExhausted]. A read error fails CLOSED (the
// session-start precedent): when the gate cannot be evaluated the call must
// not silently spend platform money.
//
// [OffSession.Begin] already runs it. Call it directly only where a request
// SCHEDULES paid work it does not run itself (an RPC that enqueues a
// generation job), so the refusal reaches the person who asked.
func (o *OffSession) Check(ctx context.Context, tenantID uuid.UUID) error {
	if o.allowance == nil {
		return nil
	}
	state, err := o.allowance.AllowanceState(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("billing: allowance check: %w", err)
	}
	if state.Exhausted() {
		return ErrAllowanceExhausted
	}
	return nil
}

// Begin opens one paid off-session call for the Tenant. It runs
// [OffSession.Check] first, so call it BEFORE the provider request. base is
// the production recorder the call's usage should also reach (nil discards).
//
// The caller records usage on [Call.Recorder] and MUST close the call —
// `defer call.Close(ctx)` right after Begin — or the usage never reaches the
// ledger.
func (o *OffSession) Begin(ctx context.Context, tenantID uuid.UUID, base observe.StageRecorder) (*Call, error) {
	if err := o.Check(ctx, tenantID); err != nil {
		return nil, err
	}
	if base == nil {
		base = observe.Discard{}
	}
	meter := spend.NewMeter(spend.Caps{}, o.log, nil, nil)
	ledger := NewLedger(tenantID, nil)
	return &Call{
		seam:     o,
		tenantID: tenantID,
		meter:    meter,
		ledger:   ledger,
		rec:      observe.TeeUsage(observe.TeeUsage(base, meter), ledger),
	}, nil
}

// Call is one open off-session paid call: a recorder to meter on, a running
// estimate for the attribution log line, and the flush that lands the usage.
type Call struct {
	seam     *OffSession
	tenantID uuid.UUID
	meter    *spend.Meter
	ledger   *Ledger
	rec      observe.StageRecorder

	mu       sync.Mutex
	soundUSD float64
}

// Recorder is the recorder to hand the provider call: it forwards to the
// production recorder AND to this call's meter and ledger.
func (c *Call) Recorder() observe.StageRecorder { return c.rec }

// Sound records one sound generation (sting or music). It is not part of
// [observe.UsageSink] — there is no pipeline capture point for it — so it
// has its own entry here; see [Ledger.SoundGeneration] for the row it writes.
func (c *Call) Sound(provider observe.Provider, model string, d time.Duration, characterCost int64) {
	c.ledger.SoundGeneration(provider, model, d, characterCost)
	c.mu.Lock()
	c.soundUSD += spend.EstimateSoundUSD(provider, model, d)
	c.mu.Unlock()
}

// EstimatedUSD is the call's running price estimate (ADR-0046: an estimate,
// never a billed amount) for the call site's attribution log line.
func (c *Call) EstimatedUSD() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.meter.Status().EstimatedUSD + c.soundUSD
}

// Close flushes the call's usage to the Usage Ledger. Run it on EVERY path
// out once the provider may have been reached: usage accrued before a failure
// was still billed. It is detached from ctx's cancellation and bounded by its
// own timeout, and it is safe to call more than once (a drained ledger
// flushes nothing).
//
// A failed write is retried once, which is safe by construction:
// [Ledger.Flush] only keeps a batch that provably did not commit, and drops
// one whose commit outcome is unknown ([storage.ErrCommitOutcomeUnknown])
// rather than risk counting it twice. Whatever is still unwritten after that
// is lost with the call; the failure is logged here and returned for callers
// that report it.
func (c *Call) Close(ctx context.Context) error {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), offSessionFlushTimeout)
	defer cancel()
	err := c.ledger.Flush(flushCtx, c.seam.usage.AddUsage)
	if err != nil && !errors.Is(err, storage.ErrCommitOutcomeUnknown) {
		err = c.ledger.Flush(flushCtx, c.seam.usage.AddUsage)
	}
	if err != nil {
		c.seam.log.Error("billing: off-session usage ledger flush failed; usage estimate lost",
			"tenant_id", c.tenantID, "err", err)
	}
	return err
}
