package address_test

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
)

// TestMatcher_ContinuationExpires covers the ADR-0024 stage-2 amendment: the
// last-speaker continuation is bounded by Config.ContinuationWindow. Inside the
// window an unnamed follow-up still lands on the previously addressed NPC;
// past it the conversation is over and the utterance routes NOWHERE, so table
// chatter minutes later no longer wakes the NPC that was addressed once.
func TestMatcher_ContinuationExpires(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{
		Language:           "en",
		Clock:              clk.now,
		ContinuationWindow: 30 * time.Second,
	}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, pour me a drink."), "npc-bart")

	// Inside the window: the follow-up continues the conversation.
	clk.advance(10 * time.Second)
	assertIDs(t, m.TargetMatch("and what else do you have?"), "npc-bart")

	// The follow-up refreshed the continuation, so the window runs from IT.
	clk.advance(20 * time.Second)
	assertIDs(t, m.TargetMatch("anything on tap?"), "npc-bart")

	// Past the window with no fresh address: the ambient continuation is dead.
	clk.advance(31 * time.Second)
	assertIDs(t, m.TargetMatch("so where were we"))
}

// TestMatcher_ContinuationWindowDefaultsToBounded pins that the shipped default
// is bounded rather than permanent: a matcher built without an explicit
// ContinuationWindow must still forget an addressee eventually. Before the
// ADR-0024 amendment lastAddressed had no timestamp at all and continuation
// survived arbitrary silence, which made an NPC answer every unnamed utterance
// for the rest of the session.
func TestMatcher_ContinuationWindowDefaultsToBounded(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{Language: "en", Clock: clk.now}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, pour me a drink."), "npc-bart")
	clk.advance(10 * time.Minute)
	assertIDs(t, m.TargetMatch("so where were we"))
}

// TestMatcher_ClearContinuation covers the Barge-in seam (ADR-0027 amendment):
// a human interrupting a speaking Agent means "stop", not "keep going", so the
// interrupted Agent loses its continuation claim and the interrupting utterance
// — which is itself a routable final — no longer lands straight back on it.
func TestMatcher_ClearContinuation(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, tell me about the cellar."), "npc-bart")
	m.ClearContinuation("npc-bart")
	assertIDs(t, m.TargetMatch("no wait, stop"))

	// An explicit name still reaches the Agent afterwards: clearing continuation
	// is not a mute.
	assertIDs(t, m.TargetMatch("Bart, go on then."), "npc-bart")
}

// TestMatcher_ClearContinuationOnlyDropsThatAgent pins that the clear is
// per-Agent: interrupting Bart must not erase a continuation that has since
// moved to the Goblin.
func TestMatcher_ClearContinuationOnlyDropsThatAgent(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Goblin, what did you see?"), "npc-goblin")
	m.ClearContinuation("npc-bart")
	assertIDs(t, m.TargetMatch("and then what?"), "npc-goblin")
}

// TestMatcher_BackchannelDoesNotRoute covers the ADR-0024 substance gate: an
// utterance made ENTIRELY of backchannel vocalization ("mhm", "äh", "aha") is
// acknowledgement, not address. It must not open an LLM+TTS turn on the ambient
// heuristics, in either configured language.
func TestMatcher_BackchannelDoesNotRoute(t *testing.T) {
	for _, tc := range []struct {
		lang  string
		open  string
		fills []string
	}{
		{lang: "en", open: "Bart, tell me about the cellar.", fills: []string{"mhm", "hmm", "uh huh", "Aha.", "erm"}},
		{lang: "de", open: "Bart, erzähl mir vom Keller.", fills: []string{"mhm", "äh", "ähm", "Aha.", "Ach so."}},
	} {
		m := address.NewMatcher(address.Config{Language: tc.lang}, butler, bart, goblin)
		assertIDs(t, m.TargetMatch(tc.open), "npc-bart")
		for _, fill := range tc.fills {
			if got := routedIDs(m.TargetMatch(fill)); len(got) != 0 {
				t.Fatalf("[%s] backchannel %q routed to %v, want nowhere", tc.lang, fill, got)
			}
		}
		// The conversation is untouched: a substantive follow-up still continues.
		assertIDs(t, m.TargetMatch("and what else is down there?"), "npc-bart")
	}
}

// TestMatcher_BackchannelStillRoutesWhenNamed pins that the substance gate is
// AMBIENT-only: "Bart!" is one short word but an explicit address, and a filler
// word carrying a name ("ja, Bart") still reaches the NPC.
func TestMatcher_BackchannelStillRoutesWhenNamed(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart!"), "npc-bart")
	assertIDs(t, m.TargetMatch("ja, Bart"), "npc-bart")
}

// TestMatcher_SubstantiveShortUtteranceStillRoutes guards the gate's blast
// radius: only known filler is dropped, never an ordinary short sentence. A
// player saying "Weiter." or "Stop." is addressing the NPC they were talking to.
func TestMatcher_SubstantiveShortUtteranceStillRoutes(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, erzähl weiter."), "npc-bart")
	assertIDs(t, m.TargetMatch("Weiter."), "npc-bart")
}

// TestMatcher_OneWordAnswerStillRoutes is the regression the substance gate must
// never break: a Character NPC asks a yes/no question and the WHOLE answer is
// one word. "Ja." is the single most common shape of a player reply, so a
// vocabulary that treats lexical affirmatives as filler makes the NPC go deaf
// exactly when it asked something — the opposite failure to the one the gate
// exists to fix.
func TestMatcher_OneWordAnswerStillRoutes(t *testing.T) {
	for _, tc := range []struct {
		lang    string
		open    string
		answers []string
	}{
		{lang: "de", open: "Bart, hast du ein Zimmer frei?", answers: []string{"Ja.", "Nein.", "Klar.", "Genau.", "Okay.", "So."}},
		{lang: "en", open: "Bart, have you a room?", answers: []string{"Yes.", "No.", "Sure.", "Right.", "Okay."}},
	} {
		for _, answer := range tc.answers {
			m := address.NewMatcher(address.Config{Language: tc.lang}, butler, bart, goblin)
			assertIDs(t, m.TargetMatch(tc.open), "npc-bart")
			if got := routedIDs(m.TargetMatch(answer)); len(got) != 1 || got[0] != "npc-bart" {
				t.Errorf("[%s] answer %q routed to %v, want npc-bart", tc.lang, answer, got)
			}
		}
	}
}

// TestMatcher_BackchannelKeepsTheExchangeAlive pins the anchor refresh: an
// acknowledged exchange must not age out while the human is plainly still
// listening. Without it, filling the window with "mhm" let the continuation
// expire and the next real follow-up routed nowhere.
func TestMatcher_BackchannelKeepsTheExchangeAlive(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{
		Language:           "de",
		Clock:              clk.now,
		ContinuationWindow: 30 * time.Second,
	}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, erzähl mir vom Keller."), "npc-bart")

	clk.advance(20 * time.Second)
	assertIDs(t, m.TargetMatch("mhm"))

	clk.advance(20 * time.Second) // 40s since the last ROUTED utterance
	assertIDs(t, m.TargetMatch("und was war da unten?"), "npc-bart")
}

// TestMatcher_BackchannelVocabularyIsLanguageScoped pins the fallback rule: a
// language WITH its own list does not also consult English, so German "Er." (a
// pronoun) and "Um." (a preposition) are ordinary words rather than the English
// fillers that share their spelling.
func TestMatcher_BackchannelVocabularyIsLanguageScoped(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, wer war das?"), "npc-bart")
	assertIDs(t, m.TargetMatch("Er."), "npc-bart")
	assertIDs(t, m.TargetMatch("Um."), "npc-bart")
}

// TestMatcher_BackchannelDoesNotResurrectAnExpiredExchange is the other half of
// the anchor refresh: filler keeps a LIVE exchange alive, it must not revive a
// dead one. lastAddressed is never cleared on expiry, so without the liveness
// check a stray "hm" minutes after the last routed turn re-armed the claim and
// the next unnamed utterance — players talking to each other — routed to an NPC
// nobody was addressing.
func TestMatcher_BackchannelDoesNotResurrectAnExpiredExchange(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{
		Language:           "de",
		Clock:              clk.now,
		ContinuationWindow: 30 * time.Second,
	}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, erzähl mir vom Keller."), "npc-bart")

	clk.advance(10 * time.Minute) // the exchange is long over
	assertIDs(t, m.TargetMatch("hm"))

	clk.advance(5 * time.Second)
	assertIDs(t, m.TargetMatch("und was machen wir jetzt?"))
}

// TestMatcher_PunctuationOnlyDoesNotRoute pins the matcher-side half of the
// substance gate: a final that tokenizes to nothing ("...", "?") is a noise
// burst, not an utterance. It must not ride last-speaker continuation to the
// NPC addressed a moment ago, and it must not keep that exchange alive either.
func TestMatcher_PunctuationOnlyDoesNotRoute(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{
		Language:           "de",
		Clock:              clk.now,
		ContinuationWindow: 30 * time.Second,
	}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, erzähl mir vom Keller."), "npc-bart")

	for _, text := range []string{"...", "?", "—", ".. ?!"} {
		clk.advance(time.Second)
		assertIDs(t, m.TargetMatch(text))
	}

	clk.advance(40 * time.Second) // well past the window since the routed turn
	assertIDs(t, m.TargetMatch("und was war da unten?"))
}

// TestMatcher_ContinuationIsSpeakerScoped pins the 2026-11-20 scope (ledger O3a):
// the speaker who addressed an NPC continues without a name; another known
// speaker's unnamed utterance routes nowhere AND does not keep the claim alive.
// Before, a second player's side talk inside the window was answered by the NPC
// and re-stamped the anchor, so the claim rolled on table chatter indefinitely.
func TestMatcher_ContinuationIsSpeakerScoped(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{Language: "de", Clock: clk.now}, butler, bart, goblin)

	assertIDs(t, m.TargetMatchFrom("alice", "Bart, was weißt du über den Drachen?"), "npc-bart")

	clk.advance(10 * time.Second)
	assertIDs(t, m.TargetMatchFrom("bob", "Hat jemand noch Chips für mich?"))
	// Alice is still in her exchange.
	assertIDs(t, m.TargetMatchFrom("alice", "Und wo lebt er?"), "npc-bart")

	// Bob's and Carol's chatter must not re-arm Alice's claim: 25 s + 10 s after
	// her last line it has expired, however much the others talked in between.
	clk.advance(25 * time.Second)
	assertIDs(t, m.TargetMatchFrom("carol", "Ich hol mir kurz was zu trinken."))
	assertIDs(t, m.TargetMatchFrom("bob", "hm"))
	clk.advance(10 * time.Second)
	assertIDs(t, m.TargetMatchFrom("alice", "Und dann?"))
}

// TestMatcher_ContinuationOwnerMovesWithANewNamedAddress: the claim follows
// whoever NAMES the Agent last — Bob naming Bart takes the exchange over.
func TestMatcher_ContinuationOwnerMovesWithANewNamedAddress(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatchFrom("alice", "Bart, was gibt es heute?"), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("bob", "Bart, und für mich?"), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("bob", "Was kostet das?"), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("alice", "Was kostet das?"))
}

// TestMatcher_ContinuationUnscopedWithoutSpeakerIDs pins the fallback: with no
// speaker attribution on either side the claim stays table-wide, exactly the
// pre-amendment behaviour.
func TestMatcher_ContinuationUnscopedWithoutSpeakerIDs(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("Bart, was gibt es heute?"), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("bob", "Und was kostet das?"), "npc-bart")

	m2 := address.NewMatcher(address.Config{Language: "de"}, butler, bart, goblin)
	assertIDs(t, m2.TargetMatchFrom("alice", "Bart, was gibt es heute?"), "npc-bart")
	assertIDs(t, m2.TargetMatchFrom("", "Und was kostet das?"), "npc-bart")
}

// TestMatcher_ExtendContinuationAnchorsToTheReply pins ledger O3b, observed
// live: the window used to count from the moment the NPC was addressed, so a
// 24 s answer left the player almost none of it and the follow-up was ignored.
// The reply path now moves the anchor while the NPC is answering.
func TestMatcher_ExtendContinuationAnchorsToTheReply(t *testing.T) {
	clk := &fakeClock{t: time.Unix(5000, 0)}
	m := address.NewMatcher(address.Config{Language: "de", Clock: clk.now}, butler, bart, goblin)
	assertIDs(t, m.TargetMatchFrom("alice", "Bart, erzähl mir die ganze Geschichte."), "npc-bart")

	// 2 s to first audio, then a sentence starts every ~6 s for 24 s.
	clk.advance(2 * time.Second)
	for i := 0; i < 5; i++ {
		m.ExtendContinuation("npc-bart")
		clk.advance(6 * time.Second)
	}
	// 32 s after the address, 6 s after the last sentence began.
	clk.advance(5 * time.Second)
	assertIDs(t, m.TargetMatchFrom("alice", "Und was geschah dann?"), "npc-bart")

	// The window still closes — measured from the reply, not from never.
	clk.advance(31 * time.Second)
	assertIDs(t, m.TargetMatchFrom("alice", "Und danach?"))
}

// TestMatcher_ExtendContinuationOnlyExtendsALiveHolder: reply activity of an
// Agent that does not hold the claim — never addressed, superseded by a later
// address, or cut off by a Barge-in — must not create or revive one.
func TestMatcher_ExtendContinuationOnlyExtendsALiveHolder(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{Language: "de", Clock: clk.now}, butler, bart, goblin)

	m.ExtendContinuation("npc-bart") // never addressed
	m.ExtendContinuation("")         // unattributed
	assertIDs(t, m.TargetMatchFrom("alice", "Und was geschah dann?"))

	// Superseded: Alice moved on to the Goblin; Bart's late audio is not a claim.
	assertIDs(t, m.TargetMatchFrom("alice", "Bart, erzähl."), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("alice", "Goblin, und du?"), "npc-goblin")
	clk.advance(40 * time.Second)
	m.ExtendContinuation("npc-bart")
	assertIDs(t, m.TargetMatchFrom("alice", "Und dann?"))

	// Barged: the cut turn's trailing audio must not undo ClearContinuation.
	assertIDs(t, m.TargetMatchFrom("alice", "Bart, erzähl."), "npc-bart")
	m.ClearContinuation("npc-bart")
	m.ExtendContinuation("npc-bart")
	assertIDs(t, m.TargetMatchFrom("alice", "Und dann?"))
}
