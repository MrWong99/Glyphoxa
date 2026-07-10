package orchestrator

import (
	"context"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// EnsembleSpeaker is the seam the [Replier] drives to run an Ensemble Turn
// (ADR-0025, #301): when one utterance addresses two or more Agents the detector
// publishes a single [voiceevent.EnsembleRouted], and the replier fans the
// candidates out into parallel speculative Drafts, races them, and lets the first
// complete non-empty draft — the Lead — take the floor and speak.
//
// Draft PURELY produces one candidate's would-be reply text: it writes no history,
// synthesizes no TTS, commits no transcript, and publishes no event, so a LOSING
// candidate commits nothing (ADR-0012's zero-commit rule made structural). "" means
// the Agent says nothing; a routing to an Agent this speaker does not hold returns
// "", nil. It must honor ctx — the losers' shared draft context is cancelled the
// instant the winner is elected, and a barge tearing down the whole ensemble
// cancels every in-flight draft.
//
// Speak renders the winning Lead's already-generated draft as that Agent's turn:
// serial per-sentence dispatch, committing ONLY the delivered text (ADR-0012), and
// returns the delivered text. dispatch synthesizes one sentence at a time (the
// [PlaybackPump]'s single-in-flight contract) and reports a cancelled turn so Speak
// stops the drain and commits only what was forwarded.
//
// The production implementation is [agent.Cast], which multiplexes both calls by
// [voiceevent.AddressTarget.AgentID] across its member Repliers.
type EnsembleSpeaker interface {
	Draft(ctx context.Context, e voiceevent.AddressRouted) (string, error)
	Speak(ctx context.Context, e voiceevent.AddressRouted, draft string, dispatch func(Reply) error) (delivered string, err error)
}

// handleEnsemble runs one [voiceevent.EnsembleRouted] decision set as ONE
// floor-holding Ensemble Turn (ADR-0025/0027, #301). It mirrors the single-route
// reactor's gates (mute pre-filter, spend cap, floor Take with its coalesce fold,
// post-Take mute re-filter) but over a SET of candidate targets, then hands the
// held floor to [Replier.runEnsemble] on its own goroutine so the bus callback does
// not block.
//
// It runs inside the bus callback, so — like [Replier.handleRouted] — every path to
// a spoken turn ends by spawning a goroutine, never by doing the turn's real-time
// work here.
func (r *Replier) handleEnsemble(ctx context.Context, bus *voiceevent.Bus, e voiceevent.EnsembleRouted) {
	ctx = voiceevent.WithTurnID(ctx, e.TurnID)

	// Mute pre-filter (#211): drop muted candidates BEFORE taking the floor. A fresh
	// slice (cap 0) so the event's Targets are never mutated.
	targets := filterMuted(e.Targets, r.mutes)

	// Every candidate muted: no turn opens, no floor churn (mirrors handleRouted).
	if len(targets) == 0 {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndMute})
		return
	}
	// Degrade to the single-route path when only one candidate survives, when no
	// ensemble speaker is wired, or when the barge-in floor is absent (an ensemble
	// is one floor-holding unit — it needs the floor). The top-scored target
	// (Targets[0] order preserved by the filter) answers via handleRouted.
	if len(targets) == 1 || r.ensemble == nil || r.floor == nil {
		r.handleRouted(ctx, bus, voiceevent.AddressRouted{
			At: e.At, Text: e.Text, TurnID: e.TurnID, Target: targets[0],
		})
		return
	}
	// Spend soft cap (#130): refuse a NEW turn before taking the floor. A single
	// pre-check is airtight (spend is monotonic).
	if r.gate != nil && !r.gate.AllowTurn() {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSpendCap})
		return
	}
	// Take the floor under the coalesce anchor Targets[0] (the top-scored). The
	// anchor names one candidate until the race elects the Lead and retargets the
	// floor ([Floor.SetHolderAgent]) — an accepted window (a VAD-split re-take
	// naming another candidate supersedes until then, RISK in the #301 plan). In the
	// SAME pre-election window a per-Agent mute cut ([Floor.YieldAgent]) of the anchor
	// Targets[0] cancels turnCtx and tears the WHOLE ensemble down (the race loop's
	// turnCtx.Done() branch returns) — correct: the ensemble is one floor-holding unit
	// (ADR-0027), and muting the current holder cuts the unit just as a barge would.
	turnCtx, release, coalesced := r.floor.Take(ctx, targets[0].AgentID)
	if coalesced {
		release() // no-op on the floor, keeps the take/release pairing honest
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSupersedeCoalesced, Text: e.Text})
		return
	}
	// Race closure (#211): the mute view can flip between the pre-Take filter and
	// this Take. Re-filter now that this turn holds the floor; if every candidate is
	// now muted, release and end with the mute reason before any goroutine.
	targets = filterMuted(targets, r.mutes)
	if len(targets) == 0 {
		release()
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndMute})
		return
	}
	go r.runEnsemble(turnCtx, release, bus, e, targets)
}

// filterMuted returns the targets whose Agent is not muted by mutes, preserving
// order, on a FRESH slice (so the caller's/event's backing array is never mutated).
// A nil mute view returns the set unchanged (a copy).
func filterMuted(targets []voiceevent.AddressTarget, mutes MuteView) []voiceevent.AddressTarget {
	out := make([]voiceevent.AddressTarget, 0, len(targets))
	for _, t := range targets {
		if mutes != nil && mutes.Muted(t.AgentID) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// runEnsemble is the held-floor half of an Ensemble Turn (#301): it fans the
// candidates out into parallel speculative Drafts, races them for the first
// complete non-empty draft (the Lead), and lets that Lead speak under the
// ensemble's original TurnID while the losing drafts are cancelled and discarded.
// It owns the floor for the whole unit — a barge cancelling turnCtx unwinds the
// drafts, the Lead's synthesis, and the playback together (ADR-0027).
func (r *Replier) runEnsemble(turnCtx context.Context, release func(), bus *voiceevent.Bus, e voiceevent.EnsembleRouted, targets []voiceevent.AddressTarget) {
	defer release()

	// The losers' drafts share this child context so the winner's election cancels
	// them all at once, independently of turnCtx (which a barge cancels).
	draftCtx, cancelDrafts := context.WithCancel(turnCtx)
	defer cancelDrafts()

	type draftResult struct {
		target voiceevent.AddressTarget
		text   string
		err    error
	}
	// BUFFERED to len(targets): a losing Draft that finishes AFTER the winner is
	// elected must never block on its send (nobody drains the channel then) — the
	// goroutines would otherwise leak (#301 RISK).
	results := make(chan draftResult, len(targets))
	for _, t := range targets {
		t := t
		go func() {
			text, err := r.ensemble.Draft(draftCtx, voiceevent.AddressRouted{
				At: e.At, Text: e.Text, TurnID: e.TurnID, Target: t,
			})
			results <- draftResult{target: t, text: text, err: err}
		}()
	}

	// Full-TEXT race (ADR-0025): the FIRST complete, non-empty draft wins. Failed or
	// empty drafts are skipped; if every candidate is exhausted with nothing to say,
	// the turn ends provider_error (only while turnCtx is still alive — a barge
	// publishes its own TurnEnded).
	var lead draftResult
	won := false
	for remaining := len(targets); remaining > 0 && !won; {
		select {
		case <-turnCtx.Done():
			return // a barge/supersede tore the whole unit down mid-race
		case res := <-results:
			remaining--
			if res.err == nil && res.text != "" {
				lead = res
				won = true
			}
		}
	}
	if !won {
		if turnCtx.Err() == nil {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndProviderError})
		}
		return
	}
	// Race the cancel: when a barge fires the same instant the winner's result lands,
	// both the buffered result and turnCtx.Done() are ready and the select above may
	// have picked the result. Re-check before electing so we never publish
	// EnsembleLead after a TurnEnded{barge} — nor let SpeakDraft commit a user message
	// for a turn nothing spoke in.
	if turnCtx.Err() != nil {
		return
	}

	// Elect the Lead: announce it (so the transcript relay attributes the turn's
	// line to the winner), retarget the floor onto it (so a mute cut / coalesce keys
	// on whoever actually speaks), and cancel the losing drafts.
	bus.Publish(voiceevent.EnsembleLead{At: time.Now(), TurnID: e.TurnID, Target: lead.target})
	r.floor.SetHolderAgent(e.TurnID, lead.target.AgentID)
	cancelDrafts()

	// Speak the Lead's draft under turnCtx. The dispatch closure mirrors
	// dispatchStream's deliver-then-commit (ADR-0012): a synth failure is non-fatal
	// but recorded (ttsFailed), and a ctx cancel — before OR after the vendor call —
	// stops the drain so Speak commits only delivered sentences.
	var ttsFailed bool
	dispatch := func(rep Reply) error {
		if err := turnCtx.Err(); err != nil {
			return err
		}
		if err := r.tts.Dispatch(turnCtx, rep.Sentence, rep.Voice); err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			if turnCtx.Err() != nil {
				return turnCtx.Err()
			}
			ttsFailed = true
			return nil
		}
		// Deliver-then-commit re-check (#363): Dispatch returns nil even when a barge
		// cancelled the turn DURING the drain (tail audio cut). Report the cancel so
		// Speak does not commit this undelivered sentence.
		if err := turnCtx.Err(); err != nil {
			return err
		}
		return nil
	}
	// Speak commits the delivered text to the Lead's own history and stops the drain
	// on a barge; the coordinator needs neither return here — the turn-end reason is
	// carried by ttsFailed (below) or the barge's own TurnEnded.
	_, _ = r.ensemble.Speak(turnCtx, voiceevent.AddressRouted{
		At: e.At, Text: e.Text, TurnID: e.TurnID, Target: lead.target,
	}, lead.text, dispatch)

	// A TTS synth failure that produced no audio under a live turn is announced as
	// the turn-end reason (mirrors dispatchStream); a barge publishes its own.
	if ttsFailed && turnCtx.Err() == nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndTTSError})
	}
}
