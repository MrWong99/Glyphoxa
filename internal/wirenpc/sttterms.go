package wirenpc

import (
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// sttHints are one Voice Session's recognition hints (#701, ADR-0042 amendment
// 2026-11-22). They are resolved ONCE per session ([resolveSTTHints]) and handed
// to both STT transports — the batch client at construction and every realtime
// dial — so an utterance is transcribed with the same hints whichever path it
// takes. The zero value sends nothing: the pre-#701 requests.
type sttHints struct {
	// terms are the names at the table by priority ([sessionSTTTerms]), already
	// through the adapter's sanitiser and caps.
	terms []string
	// language pins recognition to the Campaign Language; "" is auto-detect.
	language string
}

// clientOptions are the hints as the batch client's construction options.
func (h sttHints) clientOptions() []stteleven.Option {
	var opts []stteleven.Option
	if len(h.terms) > 0 {
		opts = append(opts, stteleven.WithTerms(h.terms))
	}
	if h.language != "" {
		opts = append(opts, stteleven.WithLanguage(h.language))
	}
	return opts
}

// streamOptions are the same hints as a stream manager's options.
func (h sttHints) streamOptions() []orchestrator.StreamManagerOption {
	var opts []orchestrator.StreamManagerOption
	if len(h.terms) > 0 {
		opts = append(opts, orchestrator.WithStreamTerms(h.terms))
	}
	if h.language != "" {
		opts = append(opts, orchestrator.WithStreamLanguage(h.language))
	}
	return opts
}

// resolveSTTHints builds the session's hints from its Config: the term list when
// [Config.STTKeyterms] is on, the Campaign Language when
// [Config.STTLanguageHint] is. The terms are returned as the adapter will send
// them ([stteleven.Keyterms] — inside the provider's limits), and dropped is how
// many of the session's distinct names that left out: in practice the tail the
// 50-term cap cut.
func resolveSTTHints(cfg Config) (h sttHints, dropped int) {
	if cfg.STTKeyterms {
		offered := sessionSTTTerms(cfg.npcs, cfg.players, cfg.language)
		h.terms = stteleven.Keyterms(offered)
		dropped = len(offered) - len(h.terms)
	}
	if cfg.STTLanguageHint {
		h.language = sttLanguageCode(cfg.language)
	}
	return h, dropped
}

// sttLanguageCode reduces a Campaign Language to the primary subtag the
// recognizer takes as language_code ("de-DE" → "de"); "" stays "" (auto-detect).
func sttLanguageCode(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(language, "-_"); i >= 0 {
		language = language[:i]
	}
	return language
}

// sessionSTTTerms lists the names the recognizer should expect in one Voice
// Session, most important first — the provider's cap keeps the head of the list:
//
//  1. the NAMES that route an utterance: every Cast NPC's, then the Butler's. A
//     big Cast must not lose an NPC's name to another NPC's third spelling;
//  2. those NPCs' aliases, in the same order — the names Address Detection
//     matches, not the truncation aliases derived from them;
//  3. the player characters' names, then their aliases. They route nothing; they
//     are there so the Transcript and the NPCs read them right.
//
// A term that is an everyday word of the Campaign Language is left out
// ([address.IsCommonWord]). The recognizer rewrites a keyterm's homophones to the
// keyterm, so an NPC literally called "Wolf" would otherwise turn every spoken
// "Wolf" into the spelling that routes as an exact name. A term already listed,
// ignoring case, is not listed twice.
func sessionSTTTerms(npcs []npcSpec, players []storage.Character, language string) []string {
	var terms []string
	seen := make(map[string]struct{})
	add := func(candidates ...string) {
		for _, c := range candidates {
			c = strings.TrimSpace(c)
			key := strings.ToLower(c)
			if _, dup := seen[key]; dup || c == "" || address.IsCommonWord(language, c) {
				continue
			}
			seen[key] = struct{}{}
			terms = append(terms, c)
		}
	}
	// castThenButler visits the Cast NPCs in roster order, then the Butler.
	castThenButler := func(visit func(npcSpec)) {
		for _, butlerPass := range []bool{false, true} {
			for _, spec := range npcs {
				if (spec.role == string(storage.AgentRoleButler)) == butlerPass {
					visit(spec)
				}
			}
		}
	}
	castThenButler(func(spec npcSpec) { add(spec.name) })
	castThenButler(func(spec npcSpec) { add(spec.aliases...) })
	for _, c := range players {
		add(c.Name)
	}
	for _, c := range players {
		add(c.Aliases...)
	}
	return terms
}
