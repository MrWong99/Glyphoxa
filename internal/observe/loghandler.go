package observe

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// disgoFilterHandler is the app-owned slog.Handler decorator that tames the
// known high-frequency disgo voice messages (A1 / observability.md
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
// A second, unrelated disgo record rides the same mechanism (#623): the
// audio-send failure from voice/audio_sender.go handleErr ("failed to send
// audio" + err, no name tag), which fires per outbound frame when the voice
// gateway is not Ready or the UDP write fails. Its LINE is rate-limited the same
// way (a reconnect window otherwise floods the console at 50 frames/s) but it is
// NOT downgraded: a send failure is a real fault, so the survivor keeps its
// original Error level. The counter still moves once per record.
type disgoFilterHandler struct {
	base slog.Handler
	// names accumulates the values of every "name" attribute seen through
	// WithAttrs (disgo tags its logger name=bot → name=voice → name=voice_conn).
	names []string
	// hooks are the metric increments, one per matched record (nil = no-op).
	hooks LogHooks
	// One limiter per matched record class, so a DAVE trickle never rate-limits
	// away the audio-send line (or vice versa). Shared by pointer across
	// derivations — see the concurrency note above.
	daveLimiter      *rateLimiter
	audioSendLimiter *rateLimiter
}

// LogHooks bundles the per-record metric increments the disgo filter feeds. A nil
// field is a no-op, so a caller that only wants the log filtering passes the zero
// value.
type LogHooks struct {
	// OnDAVEDecrypt fires once per benign DAVE-decrypt record →
	// glyphoxa_voice_dave_decrypt_errors_total.
	OnDAVEDecrypt func()
	// OnAudioSendError fires once per disgo audio-send failure record →
	// glyphoxa_voice_audio_send_errors_total (#623).
	OnAudioSendError func()
}

const (
	daveMsg          = "error while reading packet"
	daveErrSubstring = "DAVE decrypt"
	daveConnName     = "voice_conn"
	// daveLogWindow is the per-window rate limit: at most one survivor line is
	// emitted (at Debug) per window, carrying suppressed=N for the rest.
	daveLogWindow = 10 * time.Second

	// audioSendMsg is disgo's audio-send failure message (voice/audio_sender.go
	// handleErr). Unlike the DAVE record it carries no name attr, so the match is
	// message + presence of an err attr.
	audioSendMsg = "failed to send audio"
	// audioSendLogWindow rate-limits the LINE only; the counter is never limited.
	audioSendLogWindow = 10 * time.Second
)

// NewDisgoFilterHandler wraps base so the two known noisy disgo records — the
// benign DAVE-decrypt trickle and the audio-send failure burst — are rate-limited
// and counted via hooks. Every other record passes through base unchanged at its
// original level.
func NewDisgoFilterHandler(base slog.Handler, hooks LogHooks) slog.Handler {
	return &disgoFilterHandler{
		base:             base,
		hooks:            hooks,
		daveLimiter:      &rateLimiter{window: daveLogWindow},
		audioSendLimiter: &rateLimiter{window: audioSendLogWindow},
	}
}

// Enabled reports whether the base handler would handle a record at level. We do
// NOT pre-filter benign records here: slog calls Enabled once with the record's
// ORIGINAL level (Error → true) and TextHandler/JSONHandler.Handle never re-check
// level, so the actual suppression has to happen in Handle (see there).
func (h *disgoFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

// Handle applies the content filter, then delegates. For the one benign DAVE
// record it bumps the counter and rate-limits; the survivor is rewritten to
// Debug and only forwarded if the base handler is actually enabled at Debug —
// otherwise it is dropped entirely (so in prod Info/JSON the line vanishes and
// only the counter advances, which is the whole point of A1). Everything else is
// forwarded verbatim at its original level.
func (h *disgoFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	switch {
	case h.isBenignDAVE(r):
		return h.countAndLimit(ctx, r, h.hooks.OnDAVEDecrypt, h.daveLimiter, slog.LevelDebug)
	case isAudioSendFailure(r):
		// Kept at its original level: a send failure is a genuine fault (#623), so
		// prod must still see one line per window — only the burst is trimmed.
		return h.countAndLimit(ctx, r, h.hooks.OnAudioSendError, h.audioSendLimiter, r.Level)
	}
	return h.base.Handle(ctx, r)
}

// countAndLimit is the shared filter body: bump the metric hook for EVERY matched
// record, then let at most one line per limiter window through at level, carrying
// suppressed=N for the drops. A survivor whose level the base handler does not
// accept is dropped entirely — the counter is what carries the information then.
func (h *disgoFilterHandler) countAndLimit(ctx context.Context, r slog.Record, hook func(), limiter *rateLimiter, level slog.Level) error {
	if hook != nil {
		hook()
	}

	emit, suppressed := limiter.allow(r.Time)
	if !emit {
		return nil // counted, rate-limited away
	}
	if !h.base.Enabled(ctx, level) {
		// e.g. prod (Info+) with a Debug survivor: the benign trickle leaves no log
		// line at all; the metric carries the information instead.
		return nil
	}

	out := slog.NewRecord(r.Time, level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(a)
		return true
	})
	if suppressed > 0 {
		out.AddAttrs(slog.Int("suppressed", suppressed))
	}
	return h.base.Handle(ctx, out)
}

// isAudioSendFailure matches disgo's outbound audio-send failure: the exact
// message plus an err attr. There is no name tag on that logger (unlike the DAVE
// record's name=voice_conn), so message + err is the whole signature.
func isAudioSendFailure(r slog.Record) bool {
	return r.Message == audioSendMsg && hasErrAttr(r, "")
}

// hasErrAttr reports whether r carries an "err" attr, optionally containing sub.
func hasErrAttr(r slog.Record, sub string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "err" && (sub == "" || strings.Contains(a.Value.String(), sub)) {
			found = true
			return false
		}
		return true
	})
	return found
}

// isBenignDAVE matches ONLY disgo's known benign voice-receive decrypt log: the
// exact message, the inner name=voice_conn tag, and an err attr containing
// "DAVE decrypt". All three must hold so a genuine voice_conn gateway error
// (different message, or an err without DAVE decrypt) is never quieted.
func (h *disgoFilterHandler) isBenignDAVE(r slog.Record) bool {
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
	return hasErrAttr(r, daveErrSubstring)
}

// WithAttrs derives a handler that remembers any "name" attrs (so the inner
// name=voice_conn reaches isBenignDAVE) and otherwise delegates attr storage to
// base. The limiter and counter hook are shared by pointer with the parent so
// rate-limiting and the metric aggregate across all derivations.
func (h *disgoFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	names := h.names
	for _, a := range attrs {
		if a.Key == "name" {
			// copy-on-append so sibling derivations never share a backing array
			next := make([]string, len(names), len(names)+1)
			copy(next, names)
			names = append(next, a.Value.String())
		}
	}
	return &disgoFilterHandler{
		base:             h.base.WithAttrs(attrs),
		names:            names,
		hooks:            h.hooks,
		daveLimiter:      h.daveLimiter,
		audioSendLimiter: h.audioSendLimiter,
	}
}

// WithGroup delegates to base; groups do not affect the name-attr matching (the
// name tags disgo sets are plain attrs, not a group), and the shared limiter /
// counter are preserved.
func (h *disgoFilterHandler) WithGroup(name string) slog.Handler {
	return &disgoFilterHandler{
		base:             h.base.WithGroup(name),
		names:            h.names,
		hooks:            h.hooks,
		daveLimiter:      h.daveLimiter,
		audioSendLimiter: h.audioSendLimiter,
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
