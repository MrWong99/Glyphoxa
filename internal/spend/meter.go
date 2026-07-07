// Package spend is the per-Voice-Session spend meter (#130, ADR-0046): a small
// in-memory accumulator that estimates a session's provider cost from the same
// usage capture points #127 already records ([observe.UsageSink]) and enforces two
// independently opt-in per-Tenant caps.
//
// It deliberately holds session-local state, NOT a Prometheus series: ADR-0032
// forbids per-session metric labels, and the cap needs an authoritative running
// total, not a sampled counter. The [Meter] is teed alongside the production
// StageRecorder ([observe.TeeUsage]) so recording usage is unchanged; the meter is
// the second reader.
//
// Every USD figure it produces is an ESTIMATE (see prices.go): plausible enough to
// enforce an operator cap, never a billed amount.
package spend

import (
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// Caps are the two independently opt-in per-Tenant thresholds (USD). Either may be
// nil (that cap is off). When both are set the RPC enforces HardUSD >= SoftUSD;
// neither set means the meter never gates (today's behavior).
type Caps struct {
	SoftUSD *float64
	HardUSD *float64
}

// CapState is which cap (if any) a session has crossed.
type CapState string

const (
	// CapNone: under every configured cap (or no caps) — turns proceed normally.
	CapNone CapState = ""
	// CapSoft: the soft cap is crossed — the replier refuses NEW turns, in-flight
	// turns finish, transcription continues, the Session screen shows the state.
	CapSoft CapState = "soft"
	// CapHard: the hard cap is crossed — the session ends itself cleanly (a
	// deliberate policy stop, ADR-0046). Terminal; supersedes soft.
	CapHard CapState = "hard"
)

// Status is a snapshot of the meter for the RPC / Session screen. EstimatedUSD is
// labelled an estimate everywhere it surfaces.
type Status struct {
	EstimatedUSD float64
	State        CapState
	Caps         Caps
}

// Meter accumulates a Voice Session's estimated spend and evaluates the soft/hard
// caps. It implements [observe.UsageSink] so it can be teed alongside the
// production recorder. ONE mutex guards the running total, the state, and the
// warn-dedup set: the arithmetic plus a threshold-callback decision under one small
// lock is simpler to reason about than a lock-free scheme, and the hot path (a few
// float adds per turn) is nowhere near contended.
type Meter struct {
	log    *slog.Logger
	onSoft func()
	onHard func()

	mu       sync.Mutex
	totalUSD float64
	state    CapState
	caps     Caps
	warned   map[string]struct{} // price keys already warned-about (dedup)

	// Each callback fires at most once for the session's life. Belt-and-suspenders
	// with the state guard, and matches the ADR-0046 contract; the Do runs OUTSIDE
	// the mutex so a callback (which may publish an event or spawn a goroutine)
	// never runs under the accumulator lock.
	softOnce sync.Once
	hardOnce sync.Once
}

// NewMeter builds a meter for caps. log receives the unknown-price warn-once (nil
// discards). onSoft/onHard are the cap-crossed callbacks (nil is a no-op); each
// fires at most once, outside the mutex, and MUST NOT block — the session Manager
// wires them to a bus publish and a context cancel.
func NewMeter(caps Caps, log *slog.Logger, onSoft, onHard func()) *Meter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if onSoft == nil {
		onSoft = func() {}
	}
	if onHard == nil {
		onHard = func() {}
	}
	return &Meter{
		log:    log,
		onSoft: onSoft,
		onHard: onHard,
		caps:   caps,
		warned: map[string]struct{}{},
	}
}

// LLMTokens folds one completion's estimated token cost into the accumulator
// (implements [observe.UsageSink]). An unknown (provider, model) uses the
// conservative default and warns once.
func (m *Meter) LLMTokens(provider observe.Provider, model string, inputTokens, outputTokens int) {
	usd, known := llmCostUSD(provider, model, inputTokens, outputTokens)
	if !known {
		m.warnUnknown("llm", string(provider), model)
	}
	m.add(usd)
}

// TTSCharacters folds one synthesis's estimated character cost into the
// accumulator (implements [observe.UsageSink]).
func (m *Meter) TTSCharacters(provider observe.Provider, chars int) {
	usd, known := ttsCostUSD(provider, chars)
	if !known {
		m.warnUnknown("tts", string(provider), "")
	}
	m.add(usd)
}

// STTAudioSeconds folds one recognition's estimated audio-duration cost into the
// accumulator (implements [observe.UsageSink]).
func (m *Meter) STTAudioSeconds(provider observe.Provider, d time.Duration) {
	usd, known := sttCostUSD(provider, d)
	if !known {
		m.warnUnknown("stt", string(provider), "")
	}
	m.add(usd)
}

// AllowTurn reports whether a NEW Agent turn may start: true only while no cap is
// crossed. Spend is monotonic, so this is a single authoritative pre-check — once
// false it never returns true again for the session.
func (m *Meter) AllowTurn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == CapNone
}

// Status returns the estimated spend, cap state, and configured caps.
func (m *Meter) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{EstimatedUSD: m.totalUSD, State: m.state, Caps: m.caps}
}

// add records usd against the total, evaluates the caps under the lock, then fires
// any newly-due callback OUTSIDE the lock (each at most once).
func (m *Meter) add(usd float64) {
	m.mu.Lock()
	m.totalUSD += usd
	fireSoft, fireHard := m.evalLocked()
	m.mu.Unlock()

	// Fire outside the mutex; the callbacks must never block or re-enter the meter.
	if fireHard {
		m.hardOnce.Do(m.onHard)
	}
	if fireSoft {
		m.softOnce.Do(m.onSoft)
	}
}

// evalLocked advances the cap state at most one level per call and reports which
// callback newly became due. Hard supersedes soft: a single add that jumps past
// both caps transitions straight to hard and fires only onHard (the session ends
// immediately, so the soft badge would be moot). Caller holds m.mu.
func (m *Meter) evalLocked() (fireSoft, fireHard bool) {
	switch {
	case m.caps.HardUSD != nil && m.totalUSD >= *m.caps.HardUSD && m.state != CapHard:
		m.state = CapHard
		return false, true
	case m.caps.SoftUSD != nil && m.totalUSD >= *m.caps.SoftUSD && m.state == CapNone:
		m.state = CapSoft
		return true, false
	default:
		return false, false
	}
}

// warnUnknown logs the conservative-default fallback for an unpriced key exactly
// once per distinct (component, provider, model), so a live run with an unpriced
// model does not flood the log every turn.
func (m *Meter) warnUnknown(component, provider, model string) {
	key := component + "\x00" + provider + "\x00" + model
	m.mu.Lock()
	_, seen := m.warned[key]
	if !seen {
		m.warned[key] = struct{}{}
	}
	m.mu.Unlock()
	if seen {
		return
	}
	m.log.Warn("spend: no price for provider/model; using a conservative estimate default (ADR-0046)",
		"component", component, "provider", provider, "model", model)
}

var _ observe.UsageSink = (*Meter)(nil)
