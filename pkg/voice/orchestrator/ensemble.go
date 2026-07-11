package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// errReactionLookaheadAborted aborts a pre-rendered Cross-talk Reaction (#375) when
// its look-ahead-marked FIRST sentence fails to dispatch. It is deliberately a
// NON-sentinel error (not [ErrNotDelivered]): SpeakDraft treats ErrNotDelivered as
// skip-and-continue, which — for the held first sentence — would let the SECOND
// sentence enqueue on the normal path and LEAPFROG the still-playing Lead (the lane
// was never released). Returning a non-sentinel error instead stops SpeakDraft's
// drain, so the Reaction aborts as a unit. Nothing commits either way (the first
// sentence was never delivered), preserving the sentinel's commit semantics; only
// the ordering guarantee differs. It never escapes the coordinator.
var errReactionLookaheadAborted = errors.New("orchestrator: reaction look-ahead dispatch aborted")

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

// CrossTalker is the Cross-talk Reaction extension of [EnsembleSpeaker] (ADR-0025,
// #302): after an Ensemble Turn's Lead speaks, the coordinator feeds the Lead's
// delivered text to at most one other addressed Agent as Cross-talk, and that Agent
// generates a Reaction — a short affirmation, a longer disagreement, or a decline.
// The coordinator discovers this capability by a type assertion on the wired
// [EnsembleSpeaker] (r.ensemble.(CrossTalker)); a speaker that does not implement it
// runs the Lead-only Ensemble Turn of #301 unchanged.
//
// React PURELY produces the reacting Agent's would-be Reaction text: like
// [EnsembleSpeaker.Draft] it writes no history, synthesizes no TTS, and commits
// nothing, so a reaction that is never spoken (a decline, or a barge before it
// plays) commits nothing (ADR-0012). "" means the Agent declines to react. It honors
// ctx — a barge tearing the ensemble down cancels the in-flight reaction generation.
// leadName/leadText are the Lead's display name and its delivered line.
//
// SpeakReaction renders an already-generated Reaction as the reacting Agent's own
// sub-turn: serial per-sentence dispatch committing ONLY the delivered text
// (ADR-0012), returning the delivered text. It commits the SAME composite user
// message React reasoned over so the recorded turn and the prompt never drift.
type CrossTalker interface {
	React(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText string) (string, error)
	SpeakReaction(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText, reaction string, dispatch func(Reply) error) (delivered string, err error)
}

// ReactionModality is the optional pre-render classification seam of a [CrossTalker]
// (#389, ADR-0025/0027): given the reactor's generated Reaction, it reports whether
// that reactor will deliver it as channel TEXT (a voiceless Butler, an explicit
// request, or a #297-d2 long answer) rather than speech — the SAME decision the
// reactor's SpeakReaction makes internally. The coordinator discovers it by a type
// assertion on the wired speaker; a text Reaction is routed AROUND the look-ahead
// pre-render (its FIRST sentence would otherwise be synthesized — and, for text, its
// TextSink post committed — DURING the Lead's playback, before the audible-Lead gate,
// posting a Reaction to a line nobody may yet have heard, ADR-0025/0027/ADR-0012).
// Audio is held in the pump lane and discardable; a text post is irreversible, so a
// text reactor must wait for the post-gate tail. A speaker that does not implement
// this classifies every reactor as audio — the #375 pre-render path, unchanged.
type ReactionModality interface {
	ReactsAsText(agentID, utterance, reaction string) bool
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
	// pending counts the draft results not yet consumed. It carries past the race so
	// the reactor pick below knows how many results can STILL arrive on the channel —
	// keying the pick on the targets slice instead would wait on results the race loop
	// already drained (errored/empty candidates it skipped), wedging the turn forever.
	pending := len(targets)
	for pending > 0 && !won {
		select {
		case <-turnCtx.Done():
			return // a barge/supersede tore the whole unit down mid-race
		case res := <-results:
			pending--
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
	// line to the winner) and retarget the floor onto it (so a mute cut / coalesce
	// keys on whoever actually speaks).
	bus.Publish(voiceevent.EnsembleLead{At: time.Now(), TurnID: e.TurnID, Target: lead.target})
	r.floor.SetHolderAgent(e.TurnID, lead.target.AgentID)

	// Cross-talk Reaction phase (#302, ADR-0025): if the wired speaker can cross-talk
	// and at least one addressed Agent remains besides the Lead, elect exactly ONE
	// reactor and generate its Reaction DURING the Lead's playback. Without a
	// [CrossTalker] — or with no other candidate — this is the Lead-only Ensemble Turn
	// of #301, unchanged.
	rt, canReact := r.ensemble.(CrossTalker)
	remaining := targetsExcept(targets, lead.target.AgentID)

	var (
		reactor    voiceevent.AddressTarget
		hasReactor bool
	)
	if canReact && len(remaining) > 0 {
		if len(remaining) == 1 {
			// Exactly one other Agent: it is the reactor. Its speculative draft is
			// discarded (it regenerates a Reaction), so we never wait for that draft —
			// a hung/gated loser must not stall the pick.
			reactor = remaining[0]
			hasReactor = true
		} else {
			// 3+ addressed: the FASTEST remaining draft to ARRIVE names the reactor
			// (ADR-0025 — its speculative draft is discarded, it regenerates a
			// Reaction). Only pending (not-yet-consumed) results can still arrive; the
			// race loop already drained the empty/errored candidates it skipped, so we
			// bound the wait by pending to avoid blocking on a result that will never
			// come. Empty/errored arrivals are skipped here too — a candidate with
			// nothing to draft is not made the reactor. A barge tears the unit down.
			for pending > 0 && !hasReactor {
				select {
				case <-turnCtx.Done():
					cancelDrafts()
					return
				case res := <-results:
					pending--
					if res.err == nil && res.text != "" {
						reactor = res.target
						hasReactor = true
					}
				}
			}
		}
	}
	// The speculative drafts are done being raced (winner elected, reactor picked):
	// cancel them all. React runs under turnCtx (NOT draftCtx), so this never cancels
	// the reaction generation launched below.
	cancelDrafts()

	// Launch the reactor's React NOW so it generates and pre-renders TEXT while the
	// Lead's audio plays (ADR-0025), landing on a BUFFERED channel so the goroutine
	// never blocks if the unit is torn down before we read it.
	var reactCh chan reactionResult
	if hasReactor {
		reactCh = make(chan reactionResult, 1)
		go func() {
			text, err := rt.React(turnCtx, voiceevent.AddressRouted{
				At: e.At, Text: e.Text, TurnID: e.TurnID, Target: reactor,
			}, lead.target.Name, lead.text)
			reactCh <- reactionResult{text: text, err: err}
		}()
	}

	// Pump look-ahead (#375, ADR-0025): when a look-ahead pump is wired AND there is a
	// reactor, pre-render the Reaction's FIRST sentence AUDIO during the Lead's playback
	// (held in the pump lane) so its onset gap after the Lead ends is near-zero. The
	// prerender goroutine dispatches the Reaction under a child reactCtx; its first
	// sentence is marked WithPlaybackLookahead so the pump HOLDS it until releaseReaction
	// releases it below. The defer is the UNIFORM teardown for every exit — barge,
	// gate-fail, decline, happy: cancel the child ctx, wait for the goroutine, and
	// discard any held-but-unreleased audio (a keyed discard for an already-released or
	// never-primed turn is a harmless no-op). Without the pump this is the #302 legacy
	// path, byte-identical.
	lookaheadOn := r.lookahead != nil && hasReactor
	var (
		rID         string
		decision    chan reactionDecision
		s1Ch        chan string
		done        chan struct{}
		ttsErrCh    chan bool
		cancelReact func()
	)
	if lookaheadOn {
		rID = voiceevent.NewTurnID()
		var reactCtx context.Context
		reactCtx, cancelReact = context.WithCancel(turnCtx)
		decision = make(chan reactionDecision, 1)
		// s1Ch carries the Reaction's FIRST (held) sentence text from the prerender
		// goroutine to releaseReaction, so the coordinator announces its TTSInvoked at
		// RELEASE (F1) — after EnsembleReaction attributes who spoke — not at pre-render.
		s1Ch = make(chan string, 1)
		done = make(chan struct{})
		// ttsErrCh carries the Reaction's all-start-error verdict (#391) from the
		// prerender goroutine to releaseReaction: true iff every reaction sentence failed
		// to start (nothing delivered), so the coordinator ends the sub-turn tts_error
		// after the goroutine returns. Buffered so the goroutine's send never blocks.
		ttsErrCh = make(chan bool, 1)
		defer func() {
			cancelReact()
			<-done
			r.lookahead.DiscardLookahead(rID)
		}()
		go r.prerenderReaction(reactCtx, rt, e, reactor, rID, lead.target.Name, lead.text, reactCh, decision, s1Ch, done, ttsErrCh)
	}

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
			// Start-error under a LIVE ctx (#362): the sentence never produced audio,
			// so it was NOT delivered. Signal ErrNotDelivered — NOT nil, which would let
			// Speak commit an undelivered sentence (ADR-0012) — while the turn stays
			// alive so Speak keeps going with later sentences.
			ttsFailed = true
			return ErrNotDelivered
		}
		// Deliver-then-commit re-check (#362, #363): Dispatch returns nil even when a
		// barge cancelled the turn DURING the drain. The forward boundary is
		// unobservable here, so a cancel-during-drain is AMBIGUOUS — treated as
		// undelivered (accepted under-report bias). Report the cancel so Speak does not
		// commit this sentence.
		if err := turnCtx.Err(); err != nil {
			return err
		}
		return nil
	}
	// Speak commits the delivered text to the Lead's own history and stops the drain
	// on a barge.
	_, speakErr := r.ensemble.Speak(turnCtx, voiceevent.AddressRouted{
		At: e.At, Text: e.Text, TurnID: e.TurnID, Target: lead.target,
	}, lead.text, dispatch)

	// A voiceless / long / text-requested Lead (#389) delivered its draft as channel
	// TEXT with ZERO TTS dispatch: mirror the routed path's ErrTextDelivered mapping
	// and publish the text_delivered terminal so the metrics TTL sweep records a
	// SUCCESS rather than reaping a no-first-audio turn. The Lead is not audibly on the
	// wire, so no Cross-talk Reaction opens (the floor never armed) — end the unit here.
	if errors.Is(speakErr, ErrTextDelivered) {
		if turnCtx.Err() == nil {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndTextDelivered})
		}
		return
	}

	// A TTS synth failure that produced no audio under a live turn is announced as
	// the turn-end reason (mirrors dispatchStream); a barge publishes its own.
	if ttsFailed && turnCtx.Err() == nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndTTSError})
	}

	// The Reaction plays only when there is a reactor, the unit is still live (a barge
	// anywhere above tears it down and a queued Reaction after a barge is FORBIDDEN,
	// ADR-0027), and the Lead is AUDIBLY on the wire ([Floor.Speaking] — its FirstOpus
	// fired). Gating on audible delivery, not committed text, is load-bearing: an
	// all-synthesis-failed Lead now commits NOTHING (#362 — each start-errored sentence
	// returns ErrNotDelivered, so Speak skips it), and it produced no audio and no
	// FirstOpus, so the floor never armed — a Reaction played then would be UNBARGEABLE
	// (its own FirstOpus{rID} can't arm a floor whose holder turn is the Lead's) and
	// would speak AFTER the Lead's TurnEnded{tts_error}. Floor.Speaking true ⟺
	// FirstOpus(lead) fired ⟺ the barge is armed through the gap and the Reaction
	// (ADR-0027).
	if !hasReactor || turnCtx.Err() != nil || !r.floor.Speaking() {
		return
	}
	// Look-ahead path (#375): the Reaction's first-sentence audio is already held in
	// the pump lane; announce and release it for a near-zero onset gap. A text reactor
	// (#389) held nothing — releaseReaction posts it now, post-gate. The uniform defer
	// above discards anything left held on any early return here.
	if lookaheadOn {
		r.releaseReaction(turnCtx, rt, bus, e, reactor, rID, lead.target.Name, lead.text, decision, s1Ch, done, ttsErrCh)
		return
	}
	r.speakReaction(turnCtx, rt, bus, e, reactor, lead.target.Name, lead.text, reactCh)
}

// prerenderReaction runs the Cross-talk Reaction's dispatch on its own goroutine so
// its FIRST sentence's audio can be synthesized and HELD in the pump's look-ahead
// lane DURING the Lead's playback (#375, ADR-0025). It waits for the reactor's
// generated Reaction, reports the decision ("" = decline/error) to the coordinator
// on decision, and — for a non-empty Reaction under a live child ctx — speaks it,
// marking ONLY the first dispatch [voiceevent.WithPlaybackLookahead] so the pump
// holds that sentence until [Replier.releaseReaction] releases it. reactCtx is a
// child of turnCtx cancelled by the coordinator's uniform teardown (barge/gate-fail/
// decline/happy), so a torn-down unit unwinds this goroutine. It closes done on exit.
func (r *Replier) prerenderReaction(reactCtx context.Context, rt CrossTalker, e voiceevent.EnsembleRouted, reactor voiceevent.AddressTarget, rID, leadName, leadText string, reactCh <-chan reactionResult, decision chan<- reactionDecision, s1Ch chan<- string, done chan<- struct{}, ttsErrCh chan<- bool) {
	defer close(done)

	var reaction string
	select {
	case <-reactCtx.Done():
		return // the unit was torn down while the Reaction generated
	case res := <-reactCh:
		reaction = res.text // a React error surfaces as "" — treated as a decline
	}
	// Classify the reactor's modality BEFORE pre-rendering (#389): a text reactor (a
	// voiceless Butler, an explicit request, or a #297-d2 long answer) must NOT enter
	// the pre-render seam — its SpeakReaction would post to the channel chat and commit
	// to history DURING the Lead's playback, before the audible-Lead gate, reacting to
	// a line nobody may yet have heard (ADR-0025/0027/ADR-0012). Only audio Reactions
	// hold a first sentence in the pump lane; a text Reaction is posted post-gate by
	// [Replier.releaseReaction]. A speaker without [ReactionModality] classifies every
	// reactor as audio (the #375 path, unchanged). The classifier keys on the RAW
	// utterance, matching SpeakReaction's own decision.
	isText := false
	if reaction != "" {
		if mod, ok := rt.(ReactionModality); ok {
			isText = mod.ReactsAsText(reactor.AgentID, e.Text, reaction)
		}
	}
	// Report the decision so the coordinator (post-Speak) knows whether to release held
	// audio, post the text Reaction, or do nothing (decline).
	decision <- reactionDecision{text: reaction, isText: isText}
	if reaction == "" || isText || reactCtx.Err() != nil {
		return // declined, a text reactor (post-gate tail owns the post), or cut
	}

	rctx := voiceevent.WithTurnID(reactCtx, rID)
	first := true
	var ttsFailed bool
	dispatch := func(rep Reply) error {
		if err := reactCtx.Err(); err != nil {
			return err
		}
		dctx := rctx
		lookahead := first
		if first {
			// The FIRST sentence is the held one: mark it so the pump lanes it, and hand
			// its text to the coordinator so releaseReaction can announce its TTSInvoked at
			// RELEASE (F1). The send precedes r.tts.Dispatch, which BLOCKS on the pump lane
			// until release — so the coordinator has s1 before it releases.
			dctx = voiceevent.WithPlaybackLookahead(rctx)
			s1Ch <- rep.Sentence
			first = false
		}
		if err := r.tts.Dispatch(dctx, rep.Sentence, rep.Voice); err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			if reactCtx.Err() != nil {
				return reactCtx.Err()
			}
			ttsFailed = true
			if lookahead {
				// ANY start-error on the held first sentence aborts the Reaction as a unit
				// (see [errReactionLookaheadAborted]): converting ErrNotDelivered into a
				// non-sentinel error stops SpeakDraft so the second sentence can never
				// leapfrog the still-playing Lead. Nothing committed either way.
				return errReactionLookaheadAborted
			}
			// A later sentence's start-error keeps the #362 skip-and-continue semantics —
			// once s1 released, the lane is empty and playback order is already safe.
			return ErrNotDelivered
		}
		if err := reactCtx.Err(); err != nil {
			return err
		}
		return nil
	}
	delivered, _ := rt.SpeakReaction(rctx, voiceevent.AddressRouted{
		At: e.At, Text: e.Text, TurnID: e.TurnID, Target: reactor,
	}, leadName, leadText, reaction, dispatch)

	// Hand releaseReaction the all-start-error verdict (#391): true iff a sentence
	// failed to start AND nothing was delivered — the held first sentence aborted the
	// unit (delivered "") — so the coordinator ends the sub-turn tts_error. A partial
	// delivery (delivered != "") keeps current semantics (ADR-0012) and reports false.
	// A ctx-cancel (barge) sets no ttsFailed, so it also reports false; releaseReaction
	// takes its barge branch instead.
	ttsErrCh <- ttsFailed && delivered == ""
}

// releaseReaction is the coordinator tail of the look-ahead Reaction (#375): after
// the Lead is audibly on the wire, it reads the prerender goroutine's decision and —
// for a non-empty Reaction under a live turn — publishes [voiceevent.EnsembleReaction]
// and THEN releases the held first sentence (the event STRICTLY precedes the
// reaction's FirstOpus, so the transcript relay attributes the line before any audio).
// It waits for the reaction to finish, then — iff a barge cut it mid-playback —
// publishes a barge [voiceevent.TurnEnded] under the reaction's own id (legacy parity;
// the Lead's already-delivered line stays committed). A decline or a barge before the
// release publishes nothing; the uniform defer discards any held audio.
func (r *Replier) releaseReaction(turnCtx context.Context, rt CrossTalker, bus *voiceevent.Bus, e voiceevent.EnsembleRouted, reactor voiceevent.AddressTarget, rID, leadName, leadText string, decision <-chan reactionDecision, s1Ch <-chan string, done <-chan struct{}, ttsErrCh <-chan bool) {
	var d reactionDecision
	select {
	case <-turnCtx.Done():
		return // a barge tore the unit down while the Reaction generated
	case d = <-decision:
	}
	if d.text == "" || turnCtx.Err() != nil {
		return // the reactor declined, or a barge landed the instant it finished
	}

	// Text reactor (#389): nothing was pre-rendered — the whole Reaction is delivered
	// post-gate now, so its irreversible TextSink post lands AFTER the Lead ended and is
	// audibly on the wire (this tail runs only past the audible-Lead gate). This is the
	// SAME post-gate handling as the no-look-ahead [Replier.speakReaction], so look-ahead
	// on and off produce identical observable events for a text reactor.
	if d.isText {
		r.postTextReaction(turnCtx, rt, bus, e, reactor, rID, leadName, leadText, d.text)
		return
	}

	// Audio reactor: wait for the prerender goroutine to reach the held first dispatch
	// (it hands us the sentence text on s1Ch, then blocks on the pump lane). Safe:
	// reactCtx ⊂ turnCtx, so a barge cancels both and the turnCtx.Done arm returns
	// without announcing. The <-done arm is a safety net for a prerender that returned
	// WITHOUT ever holding a first sentence (its reactCtx was cut before the first
	// dispatch): done closes only after the goroutine returns, so once it fires the
	// s1Ch state is final — a non-blocking re-check distinguishes a held-then-aborted
	// first sentence (s1Ch has it — proceed) from nothing held (return, no line).
	var s1 string
	select {
	case <-turnCtx.Done():
		return
	case s1 = <-s1Ch:
	case <-done:
		select {
		case s1 = <-s1Ch:
		default:
			return
		}
	}

	// Announce the sub-turn BEFORE releasing its audio (F1): EnsembleReaction attributes
	// WHO reacts, THEN the held first sentence's TTSInvoked lands (via PublishInvoked, not
	// at pre-render), THEN the lane is released so the audio plays. So the relay creates
	// the reaction's line only after its speaker is known, and a barge before this point
	// leaves no line at all.
	bus.Publish(voiceevent.EnsembleReaction{At: time.Now(), TurnID: rID, LeadTurnID: e.TurnID, Target: reactor})
	r.tts.PublishInvoked(rID, s1)
	r.lookahead.ReleaseLookahead(rID)

	<-done // the reaction played (or aborted / was cut); the goroutine has returned

	// A barge cutting the Reaction mid-playback ends this sub-turn under its OWN id
	// (the Lead's delivered line is untouched) — legacy parity with [Replier.speakReaction].
	// The two-TurnEnded-on-barge semantics are intentional (see that method's note).
	if turnCtx.Err() != nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndBarge})
		return
	}
	// All reaction sentences failed to START (nothing delivered) — the held first
	// sentence aborted the unit under a live turn (#391). End the sub-turn tts_error,
	// mirroring the Lead, so the reaction id (already announced via EnsembleReaction,
	// but with no FirstAudio) is never reaped by the metrics TTL sweep as abandoned.
	if <-ttsErrCh {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndTTSError})
	}
}

// reactionResult is the outcome of a reactor's [CrossTalker.React] (#302): the
// would-be Reaction text ("" = decline / error) carried from the generation
// goroutine to [Replier.speakReaction].
type reactionResult struct {
	text string
	err  error
}

// reactionDecision is the prerender goroutine's report to the look-ahead coordinator
// tail (#375/#389): the reactor's generated Reaction text ("" = decline) and whether
// that reactor delivers it as TEXT (classified via [ReactionModality] BEFORE any
// pre-render). isText routes the post-gate tail: a text reactor held no audio and is
// posted by [Replier.postTextReaction]; an audio reactor's first sentence waits in the
// pump lane for [Replier.releaseReaction].
type reactionDecision struct {
	text   string
	isText bool
}

// postTextReaction delivers a text-modality Cross-talk Reaction POST-GATE (#389): the
// Lead has ended and is audibly on the wire (the caller runs only past the
// audible-Lead gate), so the reactor's irreversible TextSink post now lands after the
// audience heard the line it reacts to (ADR-0025/0027). It announces the sub-turn
// (attribution) BEFORE the post, then delegates to [CrossTalker.SpeakReaction] whose
// text branch posts to channel chat with ZERO dispatch, and maps the
// [ErrTextDelivered] sentinel to TurnEnded(text_delivered) so the metrics TTL sweep
// records a success — identical events to the no-look-ahead [Replier.speakReaction]
// text path. The dispatch closure is a guard: a text reactor never dispatches, so a
// call would mean the classifier and SpeakReaction disagree; it reports ErrNotDelivered
// rather than synthesize (defensive — should not happen). A barge landing during the
// post publishes no terminal (turnCtx cut); the Lead's line stays committed.
func (r *Replier) postTextReaction(turnCtx context.Context, rt CrossTalker, bus *voiceevent.Bus, e voiceevent.EnsembleRouted, reactor voiceevent.AddressTarget, rID, leadName, leadText, reaction string) {
	bus.Publish(voiceevent.EnsembleReaction{At: time.Now(), TurnID: rID, LeadTurnID: e.TurnID, Target: reactor})
	_, err := rt.SpeakReaction(voiceevent.WithTurnID(turnCtx, rID), voiceevent.AddressRouted{
		At: e.At, Text: e.Text, TurnID: e.TurnID, Target: reactor,
	}, leadName, leadText, reaction, func(Reply) error { return ErrNotDelivered })
	if turnCtx.Err() != nil {
		return
	}
	if errors.Is(err, ErrTextDelivered) {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndTextDelivered})
	}
}

// speakReaction is the Cross-talk Reaction sub-turn (#302, ADR-0025): it waits for
// the reactor's generated Reaction (already in flight during the Lead's playback),
// and — when non-empty and the unit is still live — mints a FRESH sub-turn id,
// announces [voiceevent.EnsembleReaction], and speaks the Reaction under that id.
// A decline (empty reaction) or a barge that fired before the Reaction dispatched
// leaves no event and no line. A barge DURING the Reaction's playback tears it down
// and ends the sub-turn with a barge [voiceevent.TurnEnded] carrying the reaction's
// own id — the Lead's already-delivered line stays committed. leadName/leadText are
// the Lead's display name and delivered draft, fed to the reactor as Cross-talk.
func (r *Replier) speakReaction(turnCtx context.Context, rt CrossTalker, bus *voiceevent.Bus, e voiceevent.EnsembleRouted, reactor voiceevent.AddressTarget, leadName, leadText string, wait <-chan reactionResult) {
	var reaction string
	select {
	case <-turnCtx.Done():
		return // a barge tore the unit down while the Reaction generated
	case res := <-wait:
		reaction = res.text // a React error surfaces as "" — treated as a decline
	}
	if reaction == "" || turnCtx.Err() != nil {
		return // the reactor declined, or a barge landed the instant it finished
	}

	// A distinct sub-turn: a FRESH id so the reaction's TTSInvoked / FirstOpus /
	// TurnEnded correlate independently and land on their own transcript line, linked
	// back to the Lead via LeadTurnID. Published immediately before the first dispatch.
	rID := voiceevent.NewTurnID()
	rctx := voiceevent.WithTurnID(turnCtx, rID)
	bus.Publish(voiceevent.EnsembleReaction{At: time.Now(), TurnID: rID, LeadTurnID: e.TurnID, Target: reactor})

	var (
		dispatched bool
		ttsFailed  bool
	)
	dispatch := func(rep Reply) error {
		if err := turnCtx.Err(); err != nil {
			return err
		}
		dispatched = true
		if err := r.tts.Dispatch(rctx, rep.Sentence, rep.Voice); err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			if turnCtx.Err() != nil {
				return turnCtx.Err()
			}
			// Start-error under a LIVE ctx (#362): NOT delivered, do not commit — but
			// the Reaction turn is still alive, so keep draining later sentences.
			ttsFailed = true
			return ErrNotDelivered
		}
		if err := turnCtx.Err(); err != nil {
			return err
		}
		return nil
	}
	delivered, reactErr := rt.SpeakReaction(rctx, voiceevent.AddressRouted{
		At: e.At, Text: e.Text, TurnID: e.TurnID, Target: reactor,
	}, leadName, leadText, reaction, dispatch)

	// A voiceless / long / text-requested reactor (#389) delivered its Reaction as
	// channel TEXT with ZERO TTS dispatch (SpeakReaction → SpeakDraft's text branch).
	// EnsembleReaction already created this sub-turn's line, so publish its
	// text_delivered terminal — otherwise the metrics TTL sweep reaps the audio-less
	// sub-turn as abandoned. Mirrors the routed path's ErrTextDelivered mapping. Mutually
	// exclusive with the #391 tts_error branch below: a text reactor never dispatches, so
	// ttsFailed stays false and delivered is the non-empty posted text.
	if errors.Is(reactErr, ErrTextDelivered) {
		if turnCtx.Err() == nil {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndTextDelivered})
		}
		return
	}

	// Sample the barge state ONCE for both terminal branches below: a barge landing
	// between them must not fire BOTH a tts_error (from a nil re-sample) and a barge
	// (from a later non-nil re-sample) for the same rID. One snapshot makes the two
	// genuinely exclusive.
	bargeErr := turnCtx.Err()

	// All reaction sentences failed to START (nothing delivered) under a live turn:
	// end the sub-turn tts_error (#391), mirroring the Lead (dispatchStream), so the
	// reaction id — already announced via EnsembleReaction, but with no FirstAudio —
	// is never reaped by the metrics TTL sweep as an abandoned/no_first_audio turn. A
	// partial delivery (delivered != "") keeps current semantics (ADR-0012): its
	// FirstAudio is the success signal, so no terminal event fires.
	if ttsFailed && delivered == "" && bargeErr == nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndTTSError})
	}

	// A barge cutting the Reaction mid-playback ends this sub-turn under its OWN id
	// (the Lead's delivered line is untouched). Only when the Reaction actually began
	// dispatching — a barge before the first sentence played nothing to interrupt.
	//
	// INTENTIONAL two TurnEnded on a reaction barge: the [BargeIn] that yielded the
	// floor also publishes TurnEnded{lead-turn, barge} (the floor's holder turn is the
	// Lead's throughout the one-unit floor, ADR-0027), and we add TurnEnded{rID, barge}
	// for the reaction sub-turn. Both are correct floor-unit semantics: the barge tore
	// down the whole unit, and each of the unit's transcript lines (the Lead's under the
	// original TurnID, the reaction's under rID) is marked ended so a late sentence can
	// clobber neither. The Lead's line — cleanly completed before the barge — keeps its
	// delivered text; the relay treats a TurnEnded after a line is delivered as a normal
	// interruption, not a re-count.
	if dispatched && bargeErr != nil {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: rID, Reason: voiceevent.TurnEndBarge})
	}
}

// targetsExcept returns the targets whose AgentID is not agentID, preserving order
// on a fresh slice — the addressed candidates that remain after the Lead is elected,
// from which the Cross-talk reactor is chosen (#302).
func targetsExcept(targets []voiceevent.AddressTarget, agentID string) []voiceevent.AddressTarget {
	out := make([]voiceevent.AddressTarget, 0, len(targets))
	for _, t := range targets {
		if t.AgentID == agentID {
			continue
		}
		out = append(out, t)
	}
	return out
}
