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
// them ([stteleven.Keyterms] — de-duplicated and inside the provider's limits),
// and dropped is how many of the session's names that left out.
func resolveSTTHints(cfg Config) (h sttHints, dropped int) {
	if cfg.STTKeyterms {
		offered := sessionSTTTerms(cfg.npcs, cfg.playerCharacterTerms, cfg.language)
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
// Session, most important first: every Cast NPC's name and aliases — the names
// Address Detection matches, not the truncation aliases derived from them — then
// the Butler's, then the player characters' names and aliases (playerTerms, from
// [playerCharacterTerms]).
//
// A term that is an everyday word of the Campaign Language is left out
// ([address.IsCommonWord]). The recognizer rewrites a keyterm's homophones to the
// keyterm, so an NPC literally called "Wolf" would otherwise turn every spoken
// "Wolf" into the spelling that routes as an exact name.
func sessionSTTTerms(npcs []npcSpec, playerTerms []string, language string) []string {
	var terms []string
	add := func(candidates ...string) {
		for _, c := range candidates {
			c = strings.TrimSpace(c)
			if c == "" || address.IsCommonWord(language, c) {
				continue
			}
			terms = append(terms, c)
		}
	}
	isButler := func(spec npcSpec) bool { return spec.role == string(storage.AgentRoleButler) }
	for _, butlerPass := range []bool{false, true} {
		for _, spec := range npcs {
			if isButler(spec) == butlerPass {
				add(spec.name)
				add(spec.aliases...)
			}
		}
	}
	add(playerTerms...)
	return terms
}

// playerCharacterTerms flattens the campaign's player characters to the names
// said at the table: each character's name, then its aliases.
func playerCharacterTerms(chars []storage.Character) []string {
	var terms []string
	for _, c := range chars {
		terms = append(terms, c.Name)
		terms = append(terms, c.Aliases...)
	}
	return terms
}
