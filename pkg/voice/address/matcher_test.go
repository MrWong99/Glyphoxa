package address_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Fixed Agents reused across the matcher tests. The Butler is AddressOnly (the
// CONTEXT.md default), so only an explicit name reaches it; Bart and the Goblin
// are Character NPCs reachable by the ambient heuristics too. Bart carries
// tavern Expertise for the expert-on-recent-word cases.
var (
	butler = address.Agent{
		Target:      voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Glyphoxa"},
		AddressOnly: true,
	}
	bart = address.Agent{
		Target:    voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"},
		Expertise: []string{"tavern", "ale"},
	}
	goblin = address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-goblin", AgentRole: "character", Name: "Goblin"},
	}
	// dwarf is a spare Character NPC used to keep the lone-NPC fallback inert in
	// roster tests, so an unnamed turn routing somewhere proves continuation
	// rather than the single-NPC fallback.
	dwarf = address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-dwarf", AgentRole: "character", Name: "Durin"},
	}
	// greta is a second Character NPC used by the mute tests (#225): a scene of
	// Bart + Greta reproduces the live "muted addressee re-routes" failure.
	greta = address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-greta", AgentRole: "character", Name: "Greta"},
	}
)

// fakeClock is a manually advanced clock so the recency windows are driven
// deterministically rather than by wall time.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// routedIDs extracts the addressed AgentIDs from a decision set, in order.
func routedIDs(routed []voiceevent.AddressRouted) []string {
	ids := make([]string, len(routed))
	for i, r := range routed {
		ids[i] = r.Target.AgentID
	}
	return ids
}

// assertIDs fails unless the decision set's AgentIDs equal want exactly (order
// included).
func assertIDs(t *testing.T, routed []voiceevent.AddressRouted, want ...string) {
	t.Helper()
	got := routedIDs(routed)
	if len(got) != len(want) {
		t.Fatalf("addressed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addressed %v, want %v", got, want)
		}
	}
}

func TestMatcher_ExplicitNameRoutesToNPC(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("Bart, what's the special tonight?"), "npc-bart")
}

// TestMatcher_FuzzyMishearing is the headline of ADR-0024: the STT hears "bard"
// for "Bart", and the matcher still routes to Bart via the phonetic tier.
func TestMatcher_FuzzyMishearing(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("bard, what is the special tonight?"), "npc-bart")
}

// TestMatcher_ButlerIsAddressOnly proves the AddressOnly gate both ways: an
// utterance that names the Butler reaches it, while an ambient utterance in the
// same session never does — even though every fallback would otherwise apply.
func TestMatcher_ButlerIsAddressOnly(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Glyphoxa, roll a perception check for me."), "butler")

	// An unrelated utterance with no name reaches nobody — the Butler is
	// excluded from the fallbacks and the two NPCs make the lone-NPC fallback
	// inapplicable.
	assertIDs(t, m.TargetMatch("let us all roll for initiative"))
}

// TestMatcher_NameMatchSuppressesFallback guards the short-circuit end to end:
// in a lone-NPC session, naming the Butler must address only the Butler, never
// also the NPC the single-NPC fallback would otherwise grab.
func TestMatcher_NameMatchSuppressesFallback(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
	assertIDs(t, m.TargetMatch("Glyphoxa, give me a recap."), "butler")
}

// TestMatcher_LastAddressedContinuation covers the continuation heuristic: a
// follow-up with no fresh name stays with the previously addressed NPC, even in
// a multi-NPC session where the lone-NPC fallback cannot fire.
func TestMatcher_LastAddressedContinuation(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, pour me a drink."), "npc-bart")
	assertIDs(t, m.TargetMatch("and what else do you have?"), "npc-bart")
}

// TestMatcher_ButlerInterjectionDoesNotClobberContinuation guards ADR-0024's
// AddressOnly exclusion from the last-speaker heuristic: a Butler explicitly
// named mid-conversation is published, but must NEVER be recorded as
// lastAddressed. Otherwise a 2-NPC scene where the player is talking to Bart,
// interrupted by a GM "Glyphoxa, roll two d6", black-holes the player's next
// unnamed follow-up (the Butler can't score AddressOnly and the sole-NPC
// fallback is inert with two NPCs) — routing NOWHERE.
func TestMatcher_ButlerInterjectionDoesNotClobberContinuation(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, pour me a drink."), "npc-bart")
	assertIDs(t, m.TargetMatch("Glyphoxa, roll two d6."), "butler")
	// The unnamed follow-up still belongs to Bart — the Butler interjection did
	// not overwrite the continuation state.
	assertIDs(t, m.TargetMatch("and what else do you have?"), "npc-bart")
}

// TestMatcher_SoleActiveNPCFallback covers the lone-NPC fallback: with exactly
// one Character NPC active, an unnamed utterance routes to it.
func TestMatcher_SoleActiveNPCFallback(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
	assertIDs(t, m.TargetMatch("so what happens next?"), "npc-bart")
}

// TestMatcher_RecentlyInterruptedRecovery covers the interruption heuristic:
// after a participant barges in on the Goblin, the next unnamed utterance lands
// back on the Goblin — not on Bart — even with two NPCs active and no prior
// addressee.
func TestMatcher_RecentlyInterruptedRecovery(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{Language: "en", Clock: clk.now}, butler, bart, goblin)

	m.NoteInterruption("npc-goblin")
	clk.advance(3 * time.Second)
	assertIDs(t, m.TargetMatch("no wait, let me finish"), "npc-goblin")

	// Past the default 15s window the interruption no longer pulls focus. A
	// fresh matcher isolates this from the continuation the match above would
	// otherwise leave behind (the Goblin became the last addressee).
	stale := &fakeClock{t: time.Unix(1000, 0)}
	m2 := address.NewMatcher(address.Config{Language: "en", Clock: stale.now}, butler, bart, goblin)
	m2.NoteInterruption("npc-goblin")
	stale.advance(30 * time.Second)
	assertIDs(t, m2.TargetMatch("anyway, moving on"))
}

// TestMatcher_ExpertiseAloneIsInsufficient pins the weak-hint design: mentioning
// a word an NPC is expert on is not enough to address it by itself (0.5 < 1.0),
// so topic drift does not hijack routing.
func TestMatcher_ExpertiseAloneIsInsufficient(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("the tavern was burned down years ago"))
}

// TestMatcher_CustomHeuristicStack proves the stack is pluggable: a matcher
// configured with only an (up-weighted) expert-on-word heuristic addresses the
// expert NPC on a topic mention alone.
func TestMatcher_CustomHeuristicStack(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:   "en",
		Heuristics: []address.Heuristic{address.ExpertOnRecentWord{Weight: 1.0}},
	}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("pour me an ale would you"), "npc-bart")
}

// TestMatcher_EmptyHeuristicStackNeverAddresses pins that an explicitly empty
// (non-nil) stack disables routing — distinct from a nil stack, which defaults.
func TestMatcher_EmptyHeuristicStackNeverAddresses(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:   "en",
		Heuristics: []address.Heuristic{},
	}, butler, bart)
	assertIDs(t, m.TargetMatch("Bart!"))
}

// TestMatcher_MultiTargetEnsemble covers the multi-target half of the contract:
// one utterance naming two NPCs addresses both (an Ensemble Turn, ADR-0025).
// MaxTargets: -1 lifts the single-target default cap so the full named set is
// published — the proof that ensemble routing stays reachable.
func TestMatcher_MultiTargetEnsemble(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart, goblin)
	got := m.TargetMatch("Bart and the Goblin start arguing")
	assertIDs(t, got, "npc-bart", "npc-goblin")
}

// TestMatcher_SingleTargetByDefault pins the default cap: naming two NPCs in one
// utterance addresses only the top-scored one (ties break by Agent order), so a
// default-config matcher fires a single turn rather than two.
func TestMatcher_SingleTargetByDefault(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("Bart and the Goblin start arguing"), "npc-bart")
}

// TestMatcher_ExactNameOutranksPhoneticCollision pins the live misroute of
// issue #198: "gerade" collides with "Greta" under Double Metaphone (both KRT),
// so Greta clears the name threshold phonetically while Marek is named exactly.
// Both earn NameMatch's flat weight, and the roster-order tie-break handed the
// turn to Greta, who precedes Marek on the roster. Equal totals must rank by
// name similarity, so the exactly-heard name wins the single-target pick. The
// two prior Greta turns reproduce the live session's continuation state and
// prove it plays no part (LastAddressed is suppressed by AnyNameMatched).
func TestMatcher_ExactNameOutranksPhoneticCollision(t *testing.T) {
	greta := address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-greta", AgentRole: "character", Name: "Greta"},
	}
	marek := address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-marek", AgentRole: "character", Name: "Marek"},
	}
	m := address.NewMatcher(address.Config{Language: "en"}, bart, greta, marek)

	assertIDs(t, m.TargetMatch("Greta, erzähl mir von deinen Kräutern."), "npc-greta")
	assertIDs(t, m.TargetMatch("und was hilft gegen Kopfschmerzen?"), "npc-greta")

	// Session 545abb84: the exact "Marek" must outrank Greta's incidental
	// phonetic hit on "gerade".
	assertIDs(t, m.TargetMatch("Marek, was liegt gerade auf deinem Amboss?"), "npc-marek")
}

// glyfoxaButler is the Tenant Butler under its default name, used by the #400
// "Glyfoxa"-class corpus. It is AddressOnly like the production Butler.
var glyfoxaButler = address.Agent{
	Target:      voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Glyphoxa"},
	AddressOnly: true,
}

// TestMatcher_GlyfoxaPhoneticVariantRoutesToButler is the #400 "Glyfoxa"-class
// block: the ph→f STT rendering "Glyfoxa" of the Butler name "Glyphoxa" routes
// to the Butler, with and without a leading filler word ("So,", "Also,",
// "Ähm,"). Under Kölner Phonetik "glyphoxa" and "glyfoxa" collapse to the same
// code (45348), so the Butler clears the name threshold on the phonetic tier and
// filler position is irrelevant (a configured name matches at any offset, not
// only utterance-initial). These cases already routed correctly before the fix
// and pin that they keep doing so.
func TestMatcher_GlyfoxaPhoneticVariantRoutesToButler(t *testing.T) {
	for _, utter := range []string{
		"Glyfoxa, roll a d6",
		"So, Glyfoxa, roll a d6",
		"Also, Glyfoxa, roll a d6",
		"Ähm, Glyfoxa, roll a d6",
	} {
		m := address.NewMatcher(address.Config{Language: "de"}, glyfoxaButler, bart)
		assertIDs(t, m.TargetMatch(utter), "butler")
	}
}

// TestMatcher_ExactCharacterBeatsPhoneticButlerCollision is the #400/#413
// counter-case that bounds the fix: an exactly-addressed Character must NOT lose
// to a Butler that merely PHONETICALLY collides with a topic word. NameMatch is a
// FLAT weight so both total 1.0, but the raw-similarity tie-break separates them —
// the exact Character (1.0) outranks the phonetic Butler (0.9). Rows:
//
//   - "Gesa, wie geht es dir?"        — "geht" ≡ "Gott" (Kölner code 42)
//   - "Gesa, das ist eine gute Idee." — "gute" ≡ "Gott"
//   - "Philipp, wie geht es deinem Bruder?"
//   - "So, Glyfoxa, was hat Bart …?"  — the #400 live line: the Butler is only a
//     ph→f phonetic hit ("Glyfoxa" ≡ "Glyphoxa", 0.9) while "Bart" is exact (1.0),
//     so the exactly-heard Character wins. This is the accepted topic-mention
//     trade-off: a phonetic Butler collision cannot outrank an exact Character.
//
// Without the score-primary rule a common-word Butler name would steal every ask
// that happens to contain a phonetically-colliding word — the regression this
// pins shut.
func TestMatcher_ExactCharacterBeatsPhoneticButlerCollision(t *testing.T) {
	cases := []struct {
		utter, want string
	}{
		{"Gesa, wie geht es dir?", "npc-gesa"},
		{"Gesa, das ist eine gute Idee.", "npc-gesa"},
		{"Philipp, wie geht es deinem Bruder?", "npc-philipp"},
	}
	for _, tc := range cases {
		m := address.NewMatcher(address.Config{Language: "de"}, gott413, gesa413, philipp413)
		assertIDs(t, m.TargetMatch(tc.utter), tc.want)
	}
	// The #400 live line, on its own roster (Butler "Glyphoxa" + Bart).
	m := address.NewMatcher(address.Config{Language: "de"}, glyfoxaButler, bart)
	assertIDs(t, m.TargetMatch("So, Glyfoxa, was hat Bart über sein Gasthaus erzählt?"), "npc-bart")
}

// TestMatcher_EarliestAddresseeOutranksSameTierTopic pins the addressee-position
// convention that resolves same-tier co-name ties (#400/#413, reviewer finding
// #3). When two Agents match at the SAME similarity tier — both exact 1.0 — the
// one spoken FIRST is the addressee and wins the single-target slot, regardless of
// roster order:
//
//   - "Bart, ist Glyphoxa hier?" — Character addressed first, Butler named later
//     as the topic → Bart. On main the Butler is rostered first, so the plain
//     roster-order tie-break wrongly handed it the Butler; the position tie-break
//     is what corrects it (this row is RED without the fix).
//   - "Gott, was hat Gesa gerade gesagt?" — Butler addressed first, Character
//     named later as the topic → Butler, whether the Butler is rostered first or
//     LAST (the live "Kings im Ring" ask, #413).
func TestMatcher_EarliestAddresseeOutranksSameTierTopic(t *testing.T) {
	// Character addressed first, Butler is the topic → Character.
	bartFirst := address.NewMatcher(address.Config{Language: "en"}, glyfoxaButler, bart)
	assertIDs(t, bartFirst.TargetMatch("Bart, ist Glyphoxa hier?"), "npc-bart")

	// Butler addressed first, Character is the topic → Butler, both roster orders.
	butlerLast := address.NewMatcher(address.Config{Language: "de"}, gesa413, philipp413, gott413)
	assertIDs(t, butlerLast.TargetMatch("Gott, was hat Gesa gerade gesagt?"), "butler")
	butlerFirst := address.NewMatcher(address.Config{Language: "de"}, gott413, gesa413, philipp413)
	assertIDs(t, butlerFirst.TargetMatch("Gott, was hat Gesa gerade gesagt?"), "butler")
}

// TestMatcher_TruncatedNameRoutesToAgent pins the #197 live misroute
// (turn 47aecba4be320d54): STT drops the leading consonant of "Bart" to "Art",
// which falls under the rune floor and never matched. With Bart carrying the
// derived truncation alias "art", an utterance opening with "Art" now routes to
// Bart — while the same "Art" mid-sentence, on a fresh matcher, reaches nobody
// (three NPCs keep the lone-NPC fallback inert and there is no continuation).
func TestMatcher_TruncatedNameRoutesToAgent(t *testing.T) {
	bartDE := address.Agent{
		Target:            voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"},
		TruncationAliases: address.DeriveTruncationAliases("Bart"),
	}
	greta := address.Agent{
		Target:            voiceevent.AddressTarget{AgentID: "npc-greta", AgentRole: "character", Name: "Greta"},
		TruncationAliases: address.DeriveTruncationAliases("Greta"),
	}
	marek := address.Agent{
		Target:            voiceevent.AddressTarget{AgentID: "npc-marek", AgentRole: "character", Name: "Marek"},
		TruncationAliases: address.DeriveTruncationAliases("Marek"),
	}

	m := address.NewMatcher(address.Config{Language: "de"}, bartDE, greta, marek)
	assertIDs(t, m.TargetMatch("Art, wie läuft das Geschäft heute Abend?"), "npc-bart")

	fresh := address.NewMatcher(address.Config{Language: "de"}, bartDE, greta, marek)
	assertIDs(t, fresh.TargetMatch("was für eine Art von Bier hast du?"))
}

// TestMatcher_GenuineNameOutranksDerivedAlias pins the tie-break: an Agent
// genuinely named "Art" (exact 1.0) must win over an Agent whose derived alias
// "art" collides (0.99), even when the genuine "Art" is rostered AFTER — the
// name-similarity tie-break, not roster order, decides it.
func TestMatcher_GenuineNameOutranksDerivedAlias(t *testing.T) {
	bartDE := address.Agent{
		Target:            voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"},
		TruncationAliases: address.DeriveTruncationAliases("Bart"),
	}
	// "Art" is vowel-initial, so it derives no alias of its own.
	art := address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-art", AgentRole: "character", Name: "Art"},
	}
	m := address.NewMatcher(address.Config{Language: "de"}, bartDE, art)
	assertIDs(t, m.TargetMatch("Art, wie läuft das Geschäft heute Abend?"), "npc-art")
}

// #413 "Gott"-class corpus. The Butler carries a common-word / exclamation
// German name ("Gott"), voiced (not GM-gated here), alongside two fallback-
// eligible Character NPCs. gott413 is AddressOnly like the production Butler.
var (
	gott413 = address.Agent{
		Target:      voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Gott"},
		AddressOnly: true,
	}
	gesa413    = address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-gesa", AgentRole: "character", Name: "Gesa"}}
	philipp413 = address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-philipp", AgentRole: "character", Name: "Philipp"}}
)

// TestMatcher_ButlerNamedWinsOverLastSpeakerBonus is a #413 AC1 PIN (it passes
// pre-change — cited as coverage, not as fix evidence): a voiced Butler named
// "Gott" addressed by name wins even though a fallback-eligible Character (Gesa)
// holds the last-speaker bonus. Both the exact name and a phonetic STT variant
// ("Goth", Kölner code 42 ≡ "gott") clear the name threshold, which sets
// AnyNameMatched and suppresses Gesa's LastAddressed bonus, so the named Butler —
// alone in the hits, no co-named Character to tie against — stands.
func TestMatcher_ButlerNamedWinsOverLastSpeakerBonus(t *testing.T) {
	for _, ask := range []string{"Gott, was denkst du darüber?", "Goth, was denkst du darüber?"} {
		m := address.NewMatcher(address.Config{Language: "de"}, gott413, gesa413, philipp413)
		// Gesa holds the floor (last-speaker bonus) going into the Butler ask.
		assertIDs(t, m.TargetMatch("Gesa, erzähl mir von den Kräutern."), "npc-gesa")
		assertIDs(t, m.TargetMatch(ask), "butler")
	}
}

// TestMatcher_GottExclamation covers the "Gott"-class exclamation collision two
// ways. "Gott" is a common German exclamation ("Oh Gott, was war das?"), and the
// word is an EXACT match on the Butler name, so the fuzzy layer cannot tell an
// exclamation from an address.
//
//   - Gate armed, non-GM speaker: a PLAYER's "Oh Gott" is dropped before scoring
//     and reaches nobody — no Character is named, so AnyNameMatched (the Butler
//     cleared the threshold) suppresses the fallbacks and the set is empty. This
//     is gate coverage, not a property of the new tie-break.
//   - Nil gate (the rollout default): with no gate the corrected logic still
//     routes "Oh Gott" to the Butler — it is the only name matched, at position 1,
//     with no addressee-position Character to outrank it. This is the inherent
//     exact-name collision the tie-break cannot and does not resolve; the designed
//     mitigation is the configuration-time rename-collision warning, follow-up
//     #414 (AC3 seam does not extend naturally).
func TestMatcher_GottExclamation(t *testing.T) {
	const gm = "gm-1"
	gated := address.NewMatcher(address.Config{
		Language:     "de",
		ButlerGMGate: func(s string) bool { return s == gm },
	}, gott413, gesa413, philipp413)
	assertIDs(t, gated.TargetMatchFrom("player-7", "Oh Gott, was war das?"))

	nilGate := address.NewMatcher(address.Config{Language: "de"}, gott413, gesa413, philipp413)
	assertIDs(t, nilGate.TargetMatch("Oh Gott, was war das?"), "butler")
}

// TestMatcher_MaxTargetsCap proves the cap is configurable: MaxTargets: 2 keeps
// the top two of a larger named set, while MaxTargets: -1 keeps them all.
func TestMatcher_MaxTargetsCap(t *testing.T) {
	const utter = "Bart, the Goblin, and Glyphoxa all turn to look"

	capped := address.NewMatcher(address.Config{Language: "en", MaxTargets: 2}, butler, bart, goblin)
	if got := routedIDs(capped.TargetMatch(utter)); len(got) != 2 {
		t.Fatalf("MaxTargets: 2 addressed %v, want 2", got)
	}

	unlimited := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart, goblin)
	if got := routedIDs(unlimited.TargetMatch(utter)); len(got) != 3 {
		t.Fatalf("MaxTargets: -1 addressed %v, want 3", got)
	}
}

// TestMatcher_TargetMatchFrom_ButlerGateExcludesNonGM pins the matcher-side
// Butler GM-gate (S6, ADR-0024 amendment): with a ButlerGMGate configured, a
// Butler-naming utterance from a non-GM (or empty) SpeakerID reaches nobody, while
// the GM's identical utterance routes to the Butler. The gate fails closed on an
// empty SpeakerID.
func TestMatcher_TargetMatchFrom_ButlerGateExcludesNonGM(t *testing.T) {
	const gm = "gm-111"
	m := address.NewMatcher(address.Config{
		Language:     "en",
		ButlerGMGate: func(speakerID string) bool { return speakerID == gm },
	}, butler, bart)

	assertIDs(t, m.TargetMatchFrom("player-999", "Glyphoxa, roll a d6"))
	assertIDs(t, m.TargetMatchFrom("", "Glyphoxa, roll a d6"))
	assertIDs(t, m.TargetMatchFrom(gm, "Glyphoxa, roll a d6"), "butler")
}

// TestMatcher_TargetMatchFrom_ExcludedButlerFreesCoNamedSlot is the #256 reason
// the gate must move matcher-side: an excluded Butler must be dropped BEFORE the
// MaxTargets cap so it never shadows a co-named Character NPC into routing
// nowhere. A non-GM naming both the Butler and Bart addresses Bart, not nobody.
func TestMatcher_TargetMatchFrom_ExcludedButlerFreesCoNamedSlot(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:     "en",
		ButlerGMGate: func(string) bool { return false }, // nobody is the GM
	}, butler, bart)

	assertIDs(t, m.TargetMatchFrom("player", "Glyphoxa and Bart, hello there"), "npc-bart")
}

// TestMatcher_TargetMatchFrom_CharacterRoutingUnaffected pins AC3-adjacent: the
// gate touches only Butler-role candidates. Character routing is byte-identical
// regardless of the speaker.
func TestMatcher_TargetMatchFrom_CharacterRoutingUnaffected(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:     "en",
		ButlerGMGate: func(string) bool { return false },
	}, butler, bart, goblin)

	assertIDs(t, m.TargetMatchFrom("anyone", "Bart, pour me a drink"), "npc-bart")
	assertIDs(t, m.TargetMatchFrom("anyone", "Goblin, what are you plotting?"), "npc-goblin")
}

// TestMatcher_TargetMatchFrom_NilGateAddressesButler pins the rollout default: no
// ButlerGMGate means the Butler answers any speaker's explicit address (the
// pre-#280 behavior), so TargetMatchFrom matches TargetMatch.
func TestMatcher_TargetMatchFrom_NilGateAddressesButler(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
	assertIDs(t, m.TargetMatchFrom("anyone", "Glyphoxa, give me a recap"), "butler")
}

// TestMatcher_AddAgent proves an Agent added after construction is addressable:
// before the Add naming the Goblin routes to nobody (two other NPCs keep the
// lone-NPC fallback inert), after it the new NPC is reached by name.
func TestMatcher_AddAgent(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, dwarf)
	assertIDs(t, m.TargetMatch("Goblin, what are you plotting?"))

	m.Add(goblin)
	assertIDs(t, m.TargetMatch("Goblin, what are you plotting?"), "npc-goblin")
}

// TestMatcher_RemoveAgent proves a removed Agent stops being addressed and that
// its continuation state is pruned: after the Goblin holds the floor it is
// removed, and the follow-up that would otherwise continue to it routes nowhere.
// Bart and the Dwarf keep the lone-NPC fallback inert throughout, so the only
// thing that could route an unnamed turn to the Goblin is stale continuation.
func TestMatcher_RemoveAgent(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, dwarf, goblin)

	// The Goblin becomes the last addressee, so continuation would normally
	// keep the floor on it.
	assertIDs(t, m.TargetMatch("Goblin, hold the line."), "npc-goblin")

	m.Remove("npc-goblin")

	// Named again, the Goblin is gone, so nobody is addressed...
	assertIDs(t, m.TargetMatch("Goblin, hold the line."))
	// ...and the continuation state was pruned, so an unnamed follow-up does not
	// resurrect it: it routes to nobody rather than continuing to the departed
	// Goblin.
	assertIDs(t, m.TargetMatch("anyway, carry on"))
}

// TestMatcher_Add_Panics pins Add's validation: an empty AgentID and a duplicate
// AgentID are both wiring errors that must panic rather than corrupt the roster.
func TestMatcher_Add_Panics(t *testing.T) {
	assertPanics(t, "empty AgentID", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentRole: "character", Name: "Nameless"}})
	})
	assertPanics(t, "duplicate AgentID", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bartholomew"}})
	})
	// An unknown AgentRole is a wiring error: it would silently disarm the Butler
	// GM-address gate (#280), which keys off AgentRoleButler. Fail closed at Add.
	assertPanics(t, "unknown AgentRole", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-x", AgentRole: "Butler", Name: "Titlecase"}})
	})
	assertPanics(t, "empty AgentRole", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-y", AgentRole: "", Name: "Roleless"}})
	})
}

// TestMatcher_TextEchoedVerbatim proves each decision carries the utterance text
// and the decision time from the matcher's clock, satisfying the AddressRouted
// contract the detector publishes verbatim.
func TestMatcher_TextEchoedVerbatim(t *testing.T) {
	clk := &fakeClock{t: time.Unix(5000, 0)}
	m := address.NewMatcher(address.Config{Language: "en", Clock: clk.now}, butler, bart)
	const utter = "Bart, hello there"
	got := m.TargetMatch(utter)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1", len(got))
	}
	if got[0].Text != utter {
		t.Errorf("Text = %q, want %q", got[0].Text, utter)
	}
	if !got[0].At.Equal(clk.now()) {
		t.Errorf("At = %v, want clock time %v", got[0].At, clk.now())
	}
}

// TestMatcher_CustomThreshold proves the address threshold is configurable: at
// a threshold above any single ambient weight, the lone-NPC fallback alone no
// longer addresses, but combining it with an interruption (1.0 + 1.0) does.
func TestMatcher_CustomThreshold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	m := address.NewMatcher(address.Config{
		Language:         "en",
		AddressThreshold: 1.5,
		Clock:            clk.now,
	}, butler, bart)

	assertIDs(t, m.TargetMatch("what now?")) // sole-NPC 1.0 < 1.5 → nobody

	m.NoteInterruption("npc-bart")                        // +1.0
	assertIDs(t, m.TargetMatch("go on then"), "npc-bart") // 2.0 ≥ 1.5
}

// TestMatcher_UnregisteredLanguageUsesEditNet proves a language with no
// phonetic encoder still matches via the Damerau-Levenshtein net alone
// (ADR-0024), so an unknown Campaign Language degrades rather than failing.
func TestMatcher_UnregisteredLanguageUsesEditNet(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "xx"}, butler, bart, goblin)
	// "goblan" is one edit from "goblin": no phonetic encoder, but within the net.
	assertIDs(t, m.TargetMatch("the goblan lunges at you"), "npc-goblin")
}

func TestNewMatcher_Panics(t *testing.T) {
	assertPanics(t, "no agents", func() { address.NewMatcher(address.Config{}) })
	assertPanics(t, "empty AgentID", func() {
		address.NewMatcher(address.Config{}, address.Agent{
			Target: voiceevent.AddressTarget{AgentRole: "character", Name: "Nameless"},
		})
	})
	// Unknown / empty AgentRole must die at construction — an agent whose role is
	// neither AgentRoleButler nor AgentRoleCharacter would silently disarm the
	// Butler GM-address gate (#280).
	assertPanics(t, "unknown AgentRole", func() {
		address.NewMatcher(address.Config{}, address.Agent{
			Target: voiceevent.AddressTarget{AgentID: "npc-x", AgentRole: "npc", Name: "Wrongrole"},
		})
	})
	assertPanics(t, "empty AgentRole", func() {
		address.NewMatcher(address.Config{}, address.Agent{
			Target: voiceevent.AddressTarget{AgentID: "npc-y", AgentRole: "", Name: "Roleless"},
		})
	})
}

func assertPanics(t *testing.T, desc string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic, got none", desc)
		}
	}()
	fn()
}

// TestMatcher_ConcurrentHeadRemoveNeverTransfersNameScore pins the snapshot
// consistency of one TargetMatch pass (#145): the fuzzy index and the roster it
// is scored against must be captured together. Removing the HEAD agent (Alice,
// index 0) mid-match reindexes the survivor (Bob) down into Alice's old slot;
// if the pass scored a pre-Remove index against the post-Remove roster, Bob
// would inherit Alice's perfect name score and answer an utterance that named
// a departed NPC. With only the NameMatch heuristic configured, the sole legal
// outcomes are "Alice" (pre-Remove snapshot) or nobody (post-Remove snapshot)
// — never Bob.
func TestMatcher_ConcurrentHeadRemoveNeverTransfersNameScore(t *testing.T) {
	alice := address.Agent{
		Target:  voiceevent.AddressTarget{AgentID: "npc-alice", AgentRole: "character", Name: "Alice"},
		Aliases: []string{"the herbalist", "keeper of the grove"},
	}
	bob := address.Agent{
		Target:  voiceevent.AddressTarget{AgentID: "npc-bob", AgentRole: "character", Name: "Bob"},
		Aliases: []string{"the blacksmith", "warden of the gate"},
	}
	// Padding words widen the scoring pass so a concurrent Remove has a real
	// window to land in.
	const utter = "alice would you kindly tell the whole table what really happened at the old stone bridge last night before the rain came down"

	for i := 0; i < 2000; i++ {
		m := address.NewMatcher(address.Config{
			Language:   "en",
			Heuristics: []address.Heuristic{address.NameMatch{Weight: 1.0, Threshold: 0.6}},
		}, alice, bob)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Remove("npc-alice")
		}()
		got := m.TargetMatch(utter)
		wg.Wait()

		for _, r := range got {
			if r.Target.AgentID == "npc-bob" {
				t.Fatalf("iteration %d: concurrent head Remove transferred Alice's name score to Bob: routed %v", i, routedIDs(got))
			}
		}
	}
}

// TestMatcher_MutedAgentStillRoutedWhenNamed pins the core of #225: a muted
// Agent stays in the index and is still matched by name, so "Bart, hörst du
// mich?" addresses the muted Bart (the decision then flows to the reactor's mute
// gate downstream). Muting must NOT drop Bart from the matcher — that was the
// bug: his name stopped matching and the utterance re-routed to another NPC.
func TestMatcher_MutedAgentStillRoutedWhenNamed(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	m.SetMuted("npc-bart", true)
	assertIDs(t, m.TargetMatch("Bart, hörst du mich?"), "npc-bart")
}

// TestMatcher_MutedNameSuppressesAmbientReRoute pins the anti-shadow rule (#225):
// naming a muted Agent addresses ONLY that Agent and never re-routes to an
// unmuted one, and — because a muted addressee is never recorded as
// lastAddressed — a later unnamed follow-up continues to whoever legitimately
// held the floor, not into a muted black hole.
func TestMatcher_MutedNameSuppressesAmbientReRoute(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	assertIDs(t, m.TargetMatch("Greta, was denkst du?"), "npc-greta") // lastAddressed = greta
	m.SetMuted("npc-bart", true)

	// Naming the muted Bart addresses only Bart (→ reactor mute gate), never Greta.
	assertIDs(t, m.TargetMatch("Bart, hörst du mich?"), "npc-bart")
	// The muted address left lastAddressed on Greta, so an unnamed follow-up
	// continues to Greta rather than the muted Bart.
	assertIDs(t, m.TargetMatch("und was hilft gegen Kopfschmerzen?"), "npc-greta")
}

// TestMatcher_NamedUnmutedWinsOverNamedMuted pins the own-merit re-route (#225):
// "Greta und Bart" with Bart muted addresses only Greta on her own merit — the
// muted Bart's name hit is dropped before the score-sort/cap regardless of
// roster order, so it never consumes the single-target slot Greta needs.
func TestMatcher_NamedUnmutedWinsOverNamedMuted(t *testing.T) {
	for _, agents := range [][]address.Agent{{bart, greta}, {greta, bart}} {
		m := address.NewMatcher(address.Config{Language: "de"}, agents...)
		m.SetMuted("npc-bart", true)
		assertIDs(t, m.TargetMatch("Greta und Bart, was denkt ihr?"), "npc-greta")
	}
}

// TestMatcher_MutedExcludedFromAmbient pins that a muted Agent is excluded from
// the ambient heuristics (#225): with Bart muted the sole-NPC fallback sees only
// Greta, and — the variant — a Bart addressed BEFORE the mute is pruned from
// lastAddressed by the mute transition, so an unnamed follow-up routes to Greta,
// not the now-muted Bart.
func TestMatcher_MutedExcludedFromAmbient(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	m.SetMuted("npc-bart", true)
	assertIDs(t, m.TargetMatch("was passiert als nächstes?"), "npc-greta")

	m2 := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	assertIDs(t, m2.TargetMatch("Bart, was denkst du?"), "npc-bart") // lastAddressed = bart
	m2.SetMuted("npc-bart", true)                                    // transition prunes bart's lastAddressed
	assertIDs(t, m2.TargetMatch("was passiert als nächstes?"), "npc-greta")
}

// TestMatcher_UnmuteRestoresImmediately pins the unmute restore (#225 / #211
// AC): unmuting re-admits Bart to both name matching and the ambient pool at
// once — naming him routes to him, and (as one of two unmuted NPCs) an unnamed
// follow-up continues to him.
func TestMatcher_UnmuteRestoresImmediately(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	m.SetMuted("npc-bart", true)
	m.SetMuted("npc-bart", false)
	assertIDs(t, m.TargetMatch("Bart, hörst du mich?"), "npc-bart")
	assertIDs(t, m.TargetMatch("und weiter?"), "npc-bart") // continuation: Bart is ambient again
}

// TestMatcher_SetMuted_UnknownAndRemoved pins SetMuted's edge contract (#225):
// muting an unknown AgentID is a clean no-op, and Remove clears the mute flag so
// a removed-then-readded Agent starts UNMUTED — proven by the re-added Bart
// catching the sole-NPC fallback (a still-muted Agent would be excluded).
func TestMatcher_SetMuted_UnknownAndRemoved(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de"}, bart, greta)
	m.SetMuted("ghost", true) // unknown: no-op, no panic, no routing change
	assertIDs(t, m.TargetMatch("Bart, hörst du mich?"), "npc-bart")

	m.SetMuted("npc-bart", true)
	m.Remove("npc-bart")
	m.Add(bart)           // starts unmuted (Remove cleared the flag)
	m.Remove("npc-greta") // Bart is now the sole NPC
	assertIDs(t, m.TargetMatch("was passiert als nächstes?"), "npc-bart")
}

// TestMatcher_ConcurrentMuteAndTargetMatch mirrors TestMatcher_ConcurrentUse for
// the mute path (#225): many goroutines flip SetMuted while others route. Run
// under `go test -race` it pins that the mute set lives under the same mutex as
// the index and roster — no data race, no torn read of the muted map.
func TestMatcher_ConcurrentMuteAndTargetMatch(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "de", MaxTargets: -1}, bart, greta)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); m.TargetMatch("Bart und Greta reden") }()
		go func() { defer wg.Done(); m.SetMuted("npc-bart", true) }()
		go func() { defer wg.Done(); m.SetMuted("npc-bart", false) }()
		go func() { defer wg.Done(); m.SetMuted("npc-greta", true) }()
	}
	wg.Wait()
}

// TestMatcher_ConcurrentUse exercises the matcher's locking: many goroutines
// route, feed interruptions, and churn the roster at once. Run under
// `go test -race` it pins that the shared conversational state stays guarded and
// that a scoring pass sees a consistent index across a concurrent rebuild.
func TestMatcher_ConcurrentUse(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		// Each iteration churns a uniquely-named NPC so concurrent Adds never
		// collide on a duplicate AgentID (which is a panic), letting roster
		// mutation race freely against routing and interruptions.
		id := fmt.Sprintf("npc-extra-%d", i)
		extra := address.Agent{Target: voiceevent.AddressTarget{AgentID: id, AgentRole: "character", Name: "Extra" + strconv.Itoa(i)}}
		wg.Add(4)
		go func() { defer wg.Done(); m.TargetMatch("Bart and the Goblin talk") }()
		go func() { defer wg.Done(); m.NoteInterruption("npc-bart") }()
		go func() { defer wg.Done(); m.Add(extra) }()
		go func() { defer wg.Done(); m.Remove(id) }()
	}
	wg.Wait()
}
