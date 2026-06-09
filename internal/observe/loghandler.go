package observe

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// daveFilterHandler is the app-owned slog.Handler decorator that tames the one
// known-benign, high-frequency disgo voice-receive message (A1 / observability.md
// §1.5). disgo logs every undecryptable inbound packet at Error
// ("error while reading packet" with err "failed to DAVE decrypt packet: …"); on
// a healthy call this is a steady benign trickle around DAVE/MLS epoch rolls, and
// in Sprint 1 it made a human misdiagnose a working call.
//
// For exactly that record it: increments the DAVE-decrypt counter, rate-limits
// to one line per window (carrying suppressed=N), and downgrades the survivor to
// Debug. Crucially it is a CONTENT filter, not a level floor on name=voice — a
// real voice-gateway error (close codes 4006/4014, UDP failure) keeps its
// original Error level and always surfaces. Matching on message text is brittle
// across disgo bumps, so the counter + rate-limit are the durable safety net (a
// real DAVE breakdown still trips the rate alert in §2.4).
//
// Concurrency / derivation: disgo builds its logger via chained
// .With("name", …) calls, so the handler that actually receives Handle is a
// derived instance several WithAttrs deep. The rate-limiter and the increment
// hook are therefore held by POINTER and shared across every derivation; only the
// accumulated name attrs are copied per-derivation (that's how we see the inner
// name=voice_conn).
type daveFilterHandler struct {
	base slog.Handler
	// names accumulates the values of every "name" attribute seen through
	// WithAttrs (disgo tags its logger name=bot → name=voice → name=voice_conn).
	names []string
	// onDAVEDecrypt is called once per matched benign record (the metric bump).
	// Held so the Prometheus adapter (task #3) can wire glyphoxa_voice_dave_
	// decrypt_errors_total; nil is a no-op.
	onDAVEDecrypt func()
	limiter       *rateLimiter
}

const (
	daveMsg          = "error while reading packet"
	daveErrSubstring = "DAVE decrypt"
	daveConnName     = "voice_conn"
	// daveLogWindow is the per-window rate limit: at most one survivor line is
	// emitted (at Debug) per window, carrying suppressed=N for the rest.
	daveLogWindow = 10 * time.Second
)

// NewDAVEFilterHandler wraps base so the benign disgo DAVE-decrypt noise is
// downgraded to Debug + rate-limited and counted via onDAVEDecrypt (nil = no-op).
// Every other record passes through base unchanged at its original level.
func NewDAVEFilterHandler(base slog.Handler, onDAVEDecrypt func()) slog.Handler {
	return &daveFilterHandler{
		base:          base,
		onDAVEDecrypt: onDAVEDecrypt,
		limiter:       &rateLimiter{window: daveLogWindow},
	}
}

// Enabled reports whether the base handler would handle a record at level. We do
// NOT pre-filter benign records here: slog calls Enabled once with the record's
// ORIGINAL level (Error → true) and TextHandler/JSONHandler.Handle never re-check
// level, so the actual suppression has to happen in Handle (see there).
func (h *daveFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

// Handle applies the content filter, then delegates. For the one benign DAVE
// record it bumps the counter and rate-limits; the survivor is rewritten to
// Debug and only forwarded if the base handler is actually enabled at Debug —
// otherwise it is dropped entirely (so in prod Info/JSON the line vanishes and
// only the counter advances, which is the whole point of A1). Everything else is
// forwarded verbatim at its original level.
func (h *daveFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.isBenignDAVE(r) {
		return h.base.Handle(ctx, r)
	}

	if h.onDAVEDecrypt != nil {
		h.onDAVEDecrypt()
	}

	emit, suppressed := h.limiter.allow(r.Time)
	if !emit {
		return nil // counted, rate-limited away
	}
	if !h.base.Enabled(ctx, slog.LevelDebug) {
		// Prod (Info+): the benign trickle leaves no log line at all; the metric
		// carries the information instead.
		return nil
	}

	// Dev (Debug enabled): emit a single survivor at Debug, annotated with how
	// many siblings were suppressed in this window.
	down := slog.NewRecord(r.Time, slog.LevelDebug, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		down.AddAttrs(a)
		return true
	})
	if suppressed > 0 {
		down.AddAttrs(slog.Int("suppressed", suppressed))
	}
	return h.base.Handle(ctx, down)
}

// isBenignDAVE matches ONLY disgo's known benign voice-receive decrypt log: the
// exact message, the inner name=voice_conn tag, and an err attr containing
// "DAVE decrypt". All three must hold so a genuine voice_conn gateway error
// (different message, or an err without DAVE decrypt) is never quieted.
func (h *daveFilterHandler) isBenignDAVE(r slog.Record) bool {
	if r.Message != daveMsg {
		return false
	}
	hasConn := false
	for _, n := range h.names {
		if n == daveConnName {
			hasConn = true
			break
		}
	}
	if !hasConn {
		return false
	}
	hasDAVEErr := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "err" && strings.Contains(a.Value.String(), daveErrSubstring) {
			hasDAVEErr = true
			return false
		}
		return true
	})
	return hasDAVEErr
}

// WithAttrs derives a handler that remembers any "name" attrs (so the inner
// name=voice_conn reaches isBenignDAVE) and otherwise delegates attr storage to
// base. The limiter and counter hook are shared by pointer with the parent so
// rate-limiting and the metric aggregate across all derivations.
func (h *daveFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	names := h.names
	for _, a := range attrs {
		if a.Key == "name" {
			// copy-on-append so sibling derivations never share a backing array
			next := make([]string, len(names), len(names)+1)
			copy(next, names)
			names = append(next, a.Value.String())
		}
	}
	return &daveFilterHandler{
		base:          h.base.WithAttrs(attrs),
		names:         names,
		onDAVEDecrypt: h.onDAVEDecrypt,
		limiter:       h.limiter,
	}
}

// WithGroup delegates to base; groups do not affect the name-attr matching (the
// name tags disgo sets are plain attrs, not a group), and the shared limiter /
// counter are preserved.
func (h *daveFilterHandler) WithGroup(name string) slog.Handler {
	return &daveFilterHandler{
		base:          h.base.WithGroup(name),
		names:         h.names,
		onDAVEDecrypt: h.onDAVEDecrypt,
		limiter:       h.limiter,
	}
}

// rateLimiter permits one event per window and counts how many it dropped since
// the last permitted one, so the survivor can report suppressed=N. Safe for
// concurrent use.
type rateLimiter struct {
	window     time.Duration
	mu         sync.Mutex
	windowEnd  time.Time
	suppressed int
}

// allow reports whether the event at t may be emitted, and if so how many events
// were suppressed since the previous emit. The first event in a fresh window is
// permitted (suppressed reported); subsequent events in the same window are
// dropped and counted.
func (l *rateLimiter) allow(t time.Time) (emit bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.Before(l.windowEnd) {
		l.suppressed++
		return false, 0
	}
	suppressed = l.suppressed
	l.suppressed = 0
	l.windowEnd = t.Add(l.window)
	return true, suppressed
}
