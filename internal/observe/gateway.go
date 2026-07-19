package observe

import (
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// identifyWindow is the rolling span Discord meters IDENTIFYs over: a bot token
// is allowed 1000 IDENTIFYs per 24h, and exhausting that budget terminates every
// session and RESETS the token — an operator outage in central-token mode where
// all tenants share one budget (#486). RESUME does not consume it.
const identifyWindow = 24 * time.Hour

// GatewayBudget observes Discord gateway session establishments, classifying each
// as an IDENTIFY (a fresh session, budget-consuming) or a RESUME (budget-free),
// exporting Prometheus counters labeled by the non-secret bot application id, and
// logging a structured warning when one application's IDENTIFYs cross a
// configurable threshold inside the rolling 24h window (#486, ADR-0032).
//
// The token is NEVER a label or a log field — only the application id, which is a
// public, bounded identifier (one per bot; a handful across tenants), so the
// series stays within the ADR-0032 cardinality bounds.
//
// Its Record* methods fire from disgo's gateway read goroutines (the Ready /
// Resumed dispatch), so all state is mutex-guarded.
type GatewayBudget struct {
	identify *prometheus.CounterVec // application_id
	resume   *prometheus.CounterVec // application_id

	threshold int
	log       *slog.Logger
	now       func() time.Time

	mu      sync.Mutex
	windows map[string][]time.Time // appID -> IDENTIFY timestamps within the window
	warned  map[string]bool        // appID -> already warned while over threshold
}

// NewGatewayBudget builds the observer and registers its two counters on reg.
// threshold is the per-application 24h IDENTIFY count above which a warning fires;
// it should sit well below Discord's 1000/day hard limit so an operator sees the
// trend before the token is reset.
func NewGatewayBudget(reg prometheus.Registerer, threshold int, log *slog.Logger) *GatewayBudget {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	b := &GatewayBudget{
		// Process-level gateway health (namespace only, no voice subsystem), like
		// embedding_backlog / jobs_total: a gateway connect is not a voice-pipeline
		// stage. application_id is the sole, bounded label (ADR-0032).
		identify: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "gateway_identify_total",
			Help:      "Discord gateway IDENTIFYs (fresh sessions) per bot application id. Discord allows 1000/token/24h; exhaustion resets the token (#486).",
		}, []string{"application_id"}),
		resume: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "gateway_resume_total",
			Help:      "Discord gateway RESUMEs (reattached sessions) per bot application id. RESUME does not consume the IDENTIFY budget (#486).",
		}, []string{"application_id"}),
		threshold: threshold,
		log:       log,
		now:       time.Now,
		windows:   map[string][]time.Time{},
		warned:    map[string]bool{},
	}
	reg.MustRegister(b.identify, b.resume)
	return b
}

// RecordIdentify counts one IDENTIFY for appID and, if that pushes the
// application's rolling-24h count past the threshold, logs a structured warning
// once until the window drains back under it (so a churning gateway warns on the
// crossing, not on every reconnect).
func (b *GatewayBudget) RecordIdentify(appID string) {
	b.identify.WithLabelValues(appID).Inc()

	b.mu.Lock()
	now := b.now()
	w := prune(b.windows[appID], now.Add(-identifyWindow))
	w = append(w, now)
	b.windows[appID] = w

	over := len(w) > b.threshold
	warn := over && !b.warned[appID]
	if over {
		b.warned[appID] = true
	} else {
		b.warned[appID] = false
	}
	count := len(w)
	b.mu.Unlock()

	if warn {
		b.log.Warn("gateway IDENTIFY budget threshold exceeded for a bot application; approaching Discord's 1000/24h token-reset limit",
			"application_id", appID,
			"identifies_24h", count,
			"threshold", b.threshold,
		)
	}
}

// RecordResume counts one RESUME for appID. RESUME does not consume the IDENTIFY
// budget, so it never trips the threshold — it is exported purely to show that a
// reconnect reattached rather than re-identified.
func (b *GatewayBudget) RecordResume(appID string) {
	b.resume.WithLabelValues(appID).Inc()
}

// prune returns ts with every timestamp at or before cutoff dropped. The slice is
// append-ordered (oldest first), so it trims a leading run.
func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}
