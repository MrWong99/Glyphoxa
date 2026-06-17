package voicebench

// Tier classifies a bench clip by the latency hypothesis it exercises (latency.md
// §5). The benchmark reports per-tier so a dice-heavy run (which forces the
// sequential tool-loop rounds, H2) reads apart from a trivial one, and the
// reasoning-bait tier isolates the dynamic-thinking tail (H1 / B2).
type Tier string

const (
	// TierTrivial is a short reply with no tool call — the no-tool control. Its
	// llm_turn is a single LLM round; deviations flag orchestration overhead,
	// not vendor variance.
	TierTrivial Tier = "trivial"

	// TierDice triggers the dice tool, forcing ≥2 sequential LLM completions
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
	// Cassette base-names under tests/voice-cassettes/ for the keyless tier:
	// voicecassette.LoadSTT/LoadTTS/LoadLLM(t, name). A clip is cassette-COMPLETE
	// only when all three resolve; STT and LLM (prompt-hash keyed) are
	// load-bearing for the reply path. Empty = that provider has no cassette yet.
	STTCassette string
	TTSCassette string
	LLMCassette string
}

// Cassette reports whether the clip has the full STT+TTS+LLM cassette set, so
// the keyless cassette tier can drive it end-to-end with no keys.
func (c Clip) Cassette() bool {
	return c.STTCassette != "" && c.TTSCassette != "" && c.LLMCassette != ""
}

// Corpus is the bench manifest: which clips feed which tier, with the cassette
// names that back the keyless tier. It reuses tests/voice-clips/ audio (which
// drives real VAD+codec) and tests/voice-cassettes/ for the network providers,
// so tiering is about which orchestration path the clip provokes, not the audio.
//
// CASSETTE COVERAGE (what exists in tests/voice-cassettes/ today):
//   - hello-test: stt-hello-test + tts-hello-test + llm-tool-dice → COMPLETE,
//     and the only dice/tool-loop clip with a full set. The de-risk clip: the
//     LLM cassette is prompt-hash keyed, so the reply-path assembly must
//     reproduce the recorded prompt exactly — stand the rig up here first.
//   - ttrpg-intro-en/de: stt + tts + the llm-agent-greet (no-tool) cassette →
//     COMPLETE for a trivial (no tool) turn.
//   - bart-test: stt only (no tts cassette) → NOT cassette-complete; live-tier
//     only until a tts-bart-test is recorded (recording needs keys, gated).
//
// GAP: no reasoning-bait clip/cassette yet (the H1/B2 tier). Until one is
// recorded, TierReasoningBait is covered only by the live A/B
// (TestLive_ThinkingCap_AB) on the gemini adapter, not the cassette tier.
var Corpus = []Clip{
	{
		Dir: "hello-test", Tier: TierDice,
		STTCassette: "stt-hello-test", TTSCassette: "tts-hello-test", LLMCassette: "llm-tool-dice",
	},
	{
		Dir: "ttrpg-intro-en", Tier: TierTrivial,
		STTCassette: "stt-ttrpg-intro-en", TTSCassette: "tts-ttrpg-intro-en", LLMCassette: "llm-agent-greet",
	},
	{
		Dir: "ttrpg-intro-de", Tier: TierTrivial,
		STTCassette: "stt-ttrpg-intro-de", TTSCassette: "tts-ttrpg-intro-de", LLMCassette: "llm-agent-greet",
	},
	// bart-test has only an STT cassette → live tier only for now.
	{Dir: "bart-test", Tier: TierTrivial, STTCassette: "stt-bart-test"},
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
