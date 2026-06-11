//go:build bench && opus && live

// The live-tier benchmark run + absolute-SLO gate (#9 C2, ADR-0033 addendum).
// Requires `bench` (benchmark code) AND `opus` (real silero VAD needs CGO) AND
// `live` (real vendor APIs, keys required) — it runs ONLY on the nightly cron /
// pre-release workflow (bench-live.yml), never on a PR.
//
// Unlike the cassette tier (instant replay → orchestration-only spans →
// relative regression diff), the live tier drives the REAL Gemini + ElevenLabs
// stack, so its response_latency is the user-facing number the Sprint-2 B-fixes
// are judged against. It therefore owns the ABSOLUTE EngineeringSLO
// (≤1.2s p50 / ≤2.5s p95 on response_latency, via Report.CheckSLO) — the SAME
// budgets observe's A2 alert rules use (one source of truth).
//
// It emits the JSON report to $GX_BENCH_OUT (the workflow uploads it as an
// artifact), then asserts CheckSLO and fails the run on a breach so the nightly
// surfaces a vendor/latency regression.
package voicebench

import (
	"context"
	"os"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	gemini "github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// liveReportEnv names the file the live report JSON is written to; the workflow
// sets it and uploads the file as the run's latency artifact. Empty ⇒ the report
// is logged but not persisted (a local `-tags live` run without the workflow).
const liveReportEnv = "GX_BENCH_OUT"

// TestBench_LiveSLO is the live-tier gate. It drives every cassette-complete
// corpus clip through the bench-owned Conversation backed by REAL Gemini +
// ElevenLabs, reports the distribution, writes the JSON artifact, and asserts
// the absolute EngineeringSLO on response_latency. A breach reds the run.
func TestBench_LiveSLO(t *testing.T) {
	// Fail loudly (not skip) if a key is missing: this test only ever runs in the
	// live workflow, where both secrets are required — a silent skip there would
	// be a green run that measured nothing (the meaningless-green the bench
	// exists to prevent). Local `-tags live` runs without keys are not expected.
	for _, env := range []string{gemini.APIKeyEnv, stteleven.APIKeyEnv} {
		if os.Getenv(env) == "" {
			t.Fatalf("%s not set — the live tier needs real vendor keys (workflow secret)", env)
		}
	}

	r := runLiveCorpus(t)
	if r.N == 0 {
		t.Fatal("live corpus produced no turns")
	}
	for _, s := range Stages {
		if d, ok := r.Stages[s]; ok {
			t.Logf("  %-18s p50=%.1f p95=%.1f p99=%.1f n=%d", s, d.P50, d.P95, d.P99, d.N)
		}
	}

	if out := os.Getenv(liveReportEnv); out != "" {
		if err := WriteBaseline(out, r); err != nil {
			t.Fatalf("write live report → %s: %v", out, err)
		}
		t.Logf("wrote live latency report → %s", out)
	}

	// The absolute headline gate (≤1.2s p50 / ≤2.5s p95). Convert each violation
	// to a test failure so the nightly reds on a latency regression — this is the
	// SAME CheckSLO the keyless TestReport_Check_FlagsBreach proves fails on a
	// synthetic over-budget number, so that keyless proof demonstrates this gate.
	for _, v := range r.CheckSLO() {
		t.Errorf("live SLO breach: %s", v)
	}
}

// runLiveCorpus drives every cassette-complete corpus clip through the
// bench-owned Conversation with live Gemini + ElevenLabs and returns the
// aggregated live-tier report. It reuses the clip audio (real VAD+codec) and the
// same rig as the cassette tier — only the providers differ.
func runLiveCorpus(t *testing.T) Report {
	t.Helper()
	acc := NewAccumulator("live", corpusTierNames())
	for _, clip := range Corpus {
		if !clip.Cassette() {
			continue // same clip set as the cassette tier (full STT/TTS/LLM path)
		}
		runClipLive(t, clip, acc)
	}
	return acc.Build()
}

// runClipLive wires one clip's real silero VAD + live providers and drives it
// through the Driver, folding its turns into acc. The reasoning_effort cap stays
// at the B2 default ("low") so the live number reflects the shipped config.
func runClipLive(t *testing.T, clip Clip, acc *Accumulator) {
	t.Helper()
	vadStage, h, frames := voicetest.NewVADRig(t, clip.Dir)

	tap := newRecorderTap()
	target := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}
	conv := BuildConversation(RigConfig{
		Bus:      h.Bus,
		VAD:      vadStage,
		STT:      orchestrator.NewSTT(h.Bus, stteleven.New("")),
		Persona:  agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: benchVoice()},
		Provider: gemini.New(""),
		Synth:    ttseleven.New(""),
		Detector: orchestrator.NewAddressDetector(alwaysRoute{target: target}),
		Recorder: tap,
	})

	silence := silenceLike(t, frames)
	d := NewDriver(conv, h, tap, acc, silence, 20)
	if err := d.RunClip(context.Background(), frames); err != nil {
		t.Fatalf("clip %q: %v", clip.Dir, err)
	}
}
