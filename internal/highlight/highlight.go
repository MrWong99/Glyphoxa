// Package highlight is the Session Highlights moment detector (#307, Epic 8): a
// bus side-consumer that watches the live Voice Session transcript and flags the
// epic moments worth cutting a clip from the rollover tape (#306, ADR-0051).
//
// It follows internal/recall's discipline EXACTLY (ADR-0020/0026): the
// [voiceevent.STTFinal] bus callback never blocks — it hands the final to a
// latest-wins mailbox and returns, and a single worker goroutine owns all detector
// state (the rolling transcript window, the per-lane audio-feature accumulators,
// the confirm/cooldown/cap counters). The decoded-PCM tap ([Detector.PCMTap],
// wired via wire.WithPCMTap) is likewise non-blocking: it summarizes each frame's
// energy and hands it to a buffered mailbox that drops under load rather than
// stalling the audio loop.
//
// The signal is the #305 hybrid: every ClassifyEvery finals the worker asks an
// LLM classifier to score the recent transcript window (0–10), with a per-lane
// RMS-energy / zero-crossing audio summary folded into the prompt. A score at or
// above Bar for ConfirmWindows consecutive classifications promotes the moment to
// a [Trigger] — a time range plus caption material and a verbatim tape [Snapshot]
// cut AT trigger time — handed to the [Sink] (#308's persistence pipeline). A
// Cooldown suppresses back-to-back triggers and a per-session MaxCandidates cap
// stops classifying once enough moments are found. Spend is metered on the
// [observe.StageRecorder] the session Manager already tees into its spend meter
// (ADR-0046); the [orchestrator.TurnGate] is checked before every classify and a
// denied gate disarms the detector silently — a Highlight never ends a session.
//
// The detector is per-session state: construct it per wirenpc Voice Session cycle
// with [NewDetector] and defer [Detector.Close] (a leak is a #44-class bug).
package highlight

import (
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/tape"
)

// Trigger is a promoted highlight moment: a wall-clock time range, the caption
// material the #310 delivery slice needs, and a verbatim tape [tape.Snapshot] cut
// at trigger time (the window rolls out of the 120s tape, so the cut cannot wait).
// It is handed to a [Sink]; the detector never persists.
type Trigger struct {
	// At is the wall-clock moment the trigger fired (the confirming final's time).
	At time.Time
	// From, To bound the clip: From = At - Lead (build-up), To = At + Tail
	// (reaction), From clamped into the tape's retention window.
	From, To time.Time
	// Score is the classifier's 0–10 rating of the confirming window.
	Score float64
	// SpeakerIDs are the distinct Speaker Lanes (ADR-0050) heard in the window.
	SpeakerIDs []string
	// Excerpt is caption-worthy transcript text for the moment.
	Excerpt string
	// Reason is the classifier's one-line justification.
	Reason string
	// Snapshot is the tape audio for [From, To], cut at trigger time.
	Snapshot tape.Snapshot
}

// Sink consumes promoted [Trigger]s. #308's Saver implements it. HandleTrigger
// must return promptly (it runs on the detector's single worker goroutine): a real
// sink enqueues and returns, it does not do blocking I/O inline.
type Sink interface {
	HandleTrigger(Trigger)
}

// SnapshotFunc cuts a consistent tape snapshot over [from, to]. [tape.Tape.Snapshot]
// satisfies it; the detector calls it in the worker at trigger time.
type SnapshotFunc func(from, to time.Time) tape.Snapshot

// Config tunes the detector to the #305 decision. Zero values take the package
// defaults (the #305 numbers), so a caller can pass a bare Config{}.
type Config struct {
	// ClassifyEvery is how many processed finals pass between classifier calls
	// (#305: 6).
	ClassifyEvery int
	// Cooldown suppresses classification for this long after a trigger, so one
	// sustained moment yields one highlight (#305: 120s).
	Cooldown time.Duration
	// Bar is the minimum classifier score (0–10) a window must reach to count
	// toward confirmation (#305: 8.0).
	Bar float64
	// ConfirmWindows is how many consecutive at-or-above-Bar classifications
	// promote a moment to a trigger (#305: 2).
	ConfirmWindows int
	// MaxCandidates caps triggers per session; once reached the detector stops
	// classifying (#305: 10).
	MaxCandidates int
	// Lead, Tail extend the clip before/after the moment (#305: 15s / 5s).
	Lead, Tail time.Duration
	// ProviderLabel is the bounded metric label for the classifier's LLM provider
	// (ADR-0032/0046 spend attribution). wirenpc sets it from the Agent's provider
	// id; the empty value defaults to Groq at metering time.
	ProviderLabel observe.Provider
}

// #305 defaults.
const (
	defaultClassifyEvery  = 6
	defaultCooldown       = 120 * time.Second
	defaultBar            = 8.0
	defaultConfirmWindows = 2
	defaultMaxCandidates  = 10
	defaultLead           = 15 * time.Second
	defaultTail           = 5 * time.Second
)

func (c Config) withDefaults() Config {
	if c.ClassifyEvery <= 0 {
		c.ClassifyEvery = defaultClassifyEvery
	}
	if c.Cooldown <= 0 {
		c.Cooldown = defaultCooldown
	}
	if c.Bar <= 0 {
		c.Bar = defaultBar
	}
	if c.ConfirmWindows <= 0 {
		c.ConfirmWindows = defaultConfirmWindows
	}
	if c.MaxCandidates <= 0 {
		c.MaxCandidates = defaultMaxCandidates
	}
	if c.Lead <= 0 {
		c.Lead = defaultLead
	}
	if c.Tail <= 0 {
		c.Tail = defaultTail
	}
	if c.ProviderLabel == "" {
		c.ProviderLabel = observe.ProviderGroq
	}
	return c
}
