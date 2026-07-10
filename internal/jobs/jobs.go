// Package jobs is the minimal DB-backed background job runner (#286, ADR-0049).
//
// It is deliberately small: one per-kind handler registry over the `job` table's
// claim/retry/dead-letter primitives (internal/storage). Postgres is the
// coordinator — the FOR UPDATE SKIP LOCKED claim is safe across web/all replicas
// by construction (ADR-0039), and a crashed worker's expired lease is re-claimed,
// so semantics are at-least-once: HANDLERS MUST BE IDEMPOTENT or tolerate
// re-execution (ADR-0049; regenerating an image for the same Highlight is
// harmless).
//
// Each poll runs one pass: sweep exhausted-and-expired jobs to the dead-letter,
// drain every currently-runnable job through its handler, refresh the per-kind
// backlog gauges, then sleep PollInterval. A handler that returns an error is
// retried with exponential backoff until its attempts are exhausted, then
// dead-lettered with the last error kept (ADR-0049 failure policy). A panicking
// handler is recovered onto the same failure path; the loop survives.
//
// embedworker is NOT migrated onto this runner (ADR-0049 keeps it bespoke in v1);
// recap and bundle import stay synchronous RPCs. The only v1 consumer is Epic 8
// Highlight enrichment, which registers its kind later — no production kind is
// registered at boot.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// HandlerFunc processes one job's payload. It MUST be idempotent: the runner is
// at-least-once, so a handler can see the same payload twice (a lease that
// expired mid-run, a crash after side effects but before CompleteJob). A nil
// return means success (the job is completed); any error triggers retry-or-dead.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) error

// Store is the persistence surface the runner needs; *storage.Store satisfies it
// and tests fake it.
type Store interface {
	ClaimJob(ctx context.Context, kinds []string, lease time.Duration) (storage.Job, error)
	CompleteJob(ctx context.Context, id uuid.UUID) error
	RetryJob(ctx context.Context, id uuid.UUID, lastError string, runAfter time.Time) error
	MarkJobDead(ctx context.Context, id uuid.UUID, lastError string) error
	SweepExpiredJobs(ctx context.Context, kinds []string) (int, error)
	CountJobBacklog(ctx context.Context, kinds []string) (map[string]int, error)
}

// Metrics is the runner's observability sink (ADR-0032). kind is bounded by the
// handler registry, so it is a safe label; a job's id/error are NEVER metrics.
// A nil Metrics disables recording. *observe.PrometheusRecorder satisfies it.
type Metrics interface {
	// JobOutcome counts one terminal outcome for a job: done, retry or dead.
	JobOutcome(kind, outcome string)
	// JobDuration observes one handler execution's wall time (success or failure).
	JobDuration(kind string, d time.Duration)
	// SetJobBacklog publishes the current runnable count for a kind (Set-from-COUNT).
	SetJobBacklog(kind string, n int)
}

// Terminal outcomes (the JobOutcome label values).
const (
	outcomeDone  = "done"
	outcomeRetry = "retry"
	outcomeDead  = "dead"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultLease        = 5 * time.Minute

	// backoff shape (ADR-0049): base doubles per attempt up to a cap, plus jitter.
	backoffBase = 30 * time.Second
	backoffCap  = 30 * time.Minute
)

// Config tunes the runner. Zero values take the defaults.
type Config struct {
	// PollInterval is the sleep between passes once the queue is drained. Default 5s.
	PollInterval time.Duration
	// Lease is how long a claim holds a job before it is reclaimable, and the
	// per-handler execution deadline. Default 5m.
	Lease time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.Lease <= 0 {
		c.Lease = defaultLease
	}
	return c
}

// Runner drains the job queue. Construct with [New], Register handlers, then Run.
type Runner struct {
	store    Store
	metrics  Metrics
	log      *slog.Logger
	cfg      Config
	handlers map[string]HandlerFunc
	kinds    []string // sorted registered kinds; the claim/backlog filter
	started  bool
}

// New builds a Runner. metrics may be nil to disable recording. Config zero
// values take the package defaults.
func New(store Store, metrics Metrics, log *slog.Logger, cfg Config) *Runner {
	return &Runner{
		store:    store,
		metrics:  metrics,
		log:      log,
		cfg:      cfg.withDefaults(),
		handlers: make(map[string]HandlerFunc),
	}
}

// Register binds a handler to a job kind. It must be called before [Run];
// registering a duplicate kind, or registering after Run has started, panics —
// both are programmer errors in process wiring.
func (r *Runner) Register(kind string, h HandlerFunc) {
	if r.started {
		panic("jobs: Register called after Run")
	}
	if _, dup := r.handlers[kind]; dup {
		panic(fmt.Sprintf("jobs: duplicate handler for kind %q", kind))
	}
	r.handlers[kind] = h
	r.kinds = append(r.kinds, kind)
	sort.Strings(r.kinds)
}

// Run drives the poll loop until ctx is cancelled, then returns. It blocks, so
// callers launch it as a goroutine. With an empty registry it idles until ctx is
// cancelled without ever touching the Store — a runner with nothing to do makes
// no DB traffic.
func (r *Runner) Run(ctx context.Context) {
	r.started = true

	if len(r.kinds) == 0 {
		r.log.Info("job runner: no handlers registered; idling")
		<-ctx.Done()
		return
	}

	for ctx.Err() == nil {
		r.pass(ctx)

		timer := time.NewTimer(r.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pass runs one full cycle: sweep dead-letter leftovers, drain every runnable job,
// then refresh the backlog gauges.
func (r *Runner) pass(ctx context.Context) {
	if n, err := r.store.SweepExpiredJobs(ctx, r.kinds); err != nil {
		r.log.Warn("job runner: sweep expired jobs failed", "err", err)
	} else if n > 0 {
		r.log.Info("job runner: dead-lettered expired jobs", "count", n)
	}

	// Drain: claim and run until the queue reports nothing runnable (or errors).
	for ctx.Err() == nil {
		job, err := r.store.ClaimJob(ctx, r.kinds, r.cfg.Lease)
		if err != nil {
			if err != storage.ErrNotFound {
				r.log.Warn("job runner: claim failed", "err", err)
			}
			break // ErrNotFound = drained; any other error = back off to next poll
		}
		r.runOne(ctx, job)
	}

	r.refreshBacklog(ctx)
}

// refreshBacklog Sets each registered kind's backlog gauge from a COUNT (never
// Inc/Dec, ADR-0032), publishing 0 for a kind with no runnable jobs so a drained
// kind reads as empty rather than stale.
func (r *Runner) refreshBacklog(ctx context.Context) {
	if r.metrics == nil {
		return
	}
	counts, err := r.store.CountJobBacklog(ctx, r.kinds)
	if err != nil {
		r.log.Warn("job runner: count backlog failed; gauges left stale", "err", err)
		return
	}
	for _, kind := range r.kinds {
		r.metrics.SetJobBacklog(kind, counts[kind])
	}
}

// runOne executes one claimed job under a Lease-bounded deadline and routes its
// result: success completes the job; an error (or a recovered panic) retries it
// with backoff while attempts remain, else dead-letters it with the last error.
func (r *Runner) runOne(ctx context.Context, job storage.Job) {
	handler, ok := r.handlers[job.Kind]
	if !ok {
		// Only registered kinds are claimed, so this is defensive; dead-letter rather
		// than spin, since no handler will ever run it.
		r.log.Error("job runner: claimed a job with no handler; dead-lettering", "kind", job.Kind, "job_id", job.ID)
		r.markDead(ctx, job, "no handler registered for kind")
		return
	}

	handlerCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.cfg.Lease))
	defer cancel()

	start := time.Now()
	err := invoke(handlerCtx, handler, job.Payload)
	if r.metrics != nil {
		r.metrics.JobDuration(job.Kind, time.Since(start))
	}

	if err == nil {
		if cerr := r.store.CompleteJob(ctx, job.ID); cerr != nil {
			r.log.Warn("job runner: complete failed; job will be reclaimed on lease expiry", "job_id", job.ID, "err", cerr)
			return
		}
		if r.metrics != nil {
			r.metrics.JobOutcome(job.Kind, outcomeDone)
		}
		return
	}

	// Failure. attempts was incremented on claim, so it is this run's attempt number.
	if job.Attempts >= job.MaxAttempts {
		r.log.Warn("job runner: job exhausted attempts; dead-lettering", "kind", job.Kind, "job_id", job.ID, "attempts", job.Attempts, "err", err)
		r.markDead(ctx, job, err.Error())
		return
	}

	runAfter := time.Now().Add(backoff(job.Attempts))
	if rerr := r.store.RetryJob(ctx, job.ID, err.Error(), runAfter); rerr != nil {
		r.log.Warn("job runner: retry write failed; job will be reclaimed on lease expiry", "job_id", job.ID, "err", rerr)
		return
	}
	if r.metrics != nil {
		r.metrics.JobOutcome(job.Kind, outcomeRetry)
	}
}

func (r *Runner) markDead(ctx context.Context, job storage.Job, lastError string) {
	if derr := r.store.MarkJobDead(ctx, job.ID, lastError); derr != nil {
		r.log.Warn("job runner: mark-dead write failed", "job_id", job.ID, "err", derr)
		return
	}
	if r.metrics != nil {
		r.metrics.JobOutcome(job.Kind, outcomeDead)
	}
}

// invoke calls the handler, converting a panic into an error so one bad handler
// cannot crash the runner loop (at-least-once means the job simply retries).
func invoke(ctx context.Context, h HandlerFunc, payload []byte) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("job handler panicked: %v", p)
		}
	}()
	return h(ctx, json.RawMessage(payload))
}

// backoff returns the retry delay for a job on its Nth attempt: an exponential
// base (30s, doubling) capped at 30m, plus uniform jitter in [0, delay/2) to
// spread a thundering herd (ADR-0049). The result lies in [delay, 1.5*delay).
func backoff(attempts int) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	delay := backoffCap
	if shift < 63 {
		if scaled := backoffBase << shift; scaled > 0 && scaled < backoffCap {
			delay = scaled
		}
	}
	return delay + jitter(delay/2)
}

// jitter returns a uniform random duration in [0, max); a non-positive max is 0.
// math/rand/v2's global source is safe for concurrent runners.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}
