package voicebench

// Tier classifies a bench clip by the latency hypothesis it exercises (latency.md
// §5). The benchmark reports per-tier so a dice-heavy run (which forces the
// sequential tool-loop rounds, H2) reads apart from a trivial one, and the
// reasoning-bait tier isolates the dynamic-thinking tail (H1 / B2).
type Tier string

const (
	// TierTrivial is a short reply with no tool call — the no-tool control. Its
	// llm_turn is a single Gemini round; deviations flag orchestration overhead,
	// not vendor variance.
	TierTrivial Tier = "trivial"

	// TierDice triggers the dice tool, forcing ≥2 sequential Gemini completions
	// per turn (H2). The contrast against TierTrivial confirms the bimodal
	// llm_turn the tool loop creates.
	TierDice Tier = "dice"

	// TierReasoningBait is a prompt that provokes deep dynamic thinking (H1) —
	// the long-tail input the B2 thinking cap targets. The A/B knob (cap low vs
	// default) is measured against this tier.
	TierReasoningBait Tier = "reasoning_bait"
)

// Clip is one corpus entry: a clip directory under tests/voice-clips/ plus the
// tier it exercises. Audio + meta.yaml live in the directory (ADR-0020); this
// manifest only records the directory name and its bench classification, so the
// corpus stays the single source of clips while the bench adds tiering on top.
type Clip struct {
	Dir  string // directory name under tests/voice-clips/
	Tier Tier
}

// Corpus is the bench manifest: which existing clips feed which tier. It reuses
// the tests/voice-clips/ clips rather than minting new audio (new dice/bait
// clips need a paid TTS render or a recorded take — a follow-up, see the
// reasoning-bait gap below). The cassette tier replays STT/TTS/LLM from
// cassettes, so a clip's *audio* drives VAD+codec while the transcript/reply
// come from the cassette — meaning tiering is about which orchestration path the
// clip's transcript provokes, not the raw audio.
//
// GAP: there is no reasoning-bait clip yet (the H1/B2 tier). The existing corpus
// has trivial (bart-test) and dice (hello-test / two-utterance-test) but nothing
// that provokes deep thinking. Recording one ("if three travelers split a
// 17-copper tab…") + its STT/LLM cassettes is the follow-up that lets the
// cassette tier exercise B2's path keylessly; until then TierReasoningBait is
// covered only by the live A/B (TestLive_ThinkingCap_AB) on the gemini adapter.
var Corpus = []Clip{
	{Dir: "bart-test", Tier: TierTrivial},
	{Dir: "hello-test", Tier: TierDice},
	{Dir: "two-utterance-test", Tier: TierDice},
}

// ClipsFor returns the corpus clips in the given tiers (all clips if none
// given), preserving manifest order. The harness uses it to drive a tier-scoped
// run — e.g. dice-only to confirm H2's bimodal llm_turn.
func ClipsFor(tiers ...Tier) []Clip {
	if len(tiers) == 0 {
		out := make([]Clip, len(Corpus))
		copy(out, Corpus)
		return out
	}
	want := make(map[Tier]bool, len(tiers))
	for _, t := range tiers {
		want[t] = true
	}
	var out []Clip
	for _, c := range Corpus {
		if want[c.Tier] {
			out = append(out, c)
		}
	}
	return out
}
