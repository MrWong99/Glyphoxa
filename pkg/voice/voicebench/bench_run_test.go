//go:build bench && opus

// The cassette-tier benchmark run + regression gate. Requires `bench` (this is
// benchmark code) AND `opus` (real silero VAD needs CGO) — the audio/CGO CI job,
// never the default no-CGO gate (ADR-0033 addendum).
//
// It drives the cassette-complete corpus clips through the bench-owned
// Conversation (real silero VAD + codec, deterministic STUB LLM+TTS — the
// orchestration-latency floor; the LLM content doesn't affect plumbing latency,
// and stubbing sidesteps the prompt_hash-keyed cassettes that were recorded for
// isolated unit tests, not clip→agent-loop replay), then either records the
// baseline (GX_BENCH_RECORD=1) or asserts the run against the committed baseline
// as a relative regression-diff.
package voicebench

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// baselinePath is the committed cassette-tier floor, repo-relative.
const baselineRel = "pkg/voice/voicebench/testdata/baseline-cassette.json"

// TestBench_CassetteRegression is the cassette-tier gate (#9). Record with
// GX_BENCH_RECORD=1 to (re)write the baseline; otherwise it runs the corpus and
// fails on a stage whose p95 grew past the regression tolerance vs the committed
// baseline. It asserts NO absolute SLO — that's the live tier's job.
func TestBench_CassetteRegression(t *testing.T) {
	r := runCassetteCorpus(t)
	if r.N == 0 {
		t.Fatal("cassette corpus produced no turns")
	}

	path := repoPath(t, baselineRel)
	if os.Getenv("GX_BENCH_RECORD") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteBaseline(path, r); err != nil {
			t.Fatalf("write baseline: %v", err)
		}
		t.Logf("recorded cassette baseline → %s", path)
		for _, s := range Stages {
			if d, ok := r.Stages[s]; ok {
				t.Logf("  %-18s p50=%.1f p95=%.1f n=%d", s, d.P50, d.P95, d.N)
			}
		}
		return
	}

	base, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("load baseline (run once with GX_BENCH_RECORD=1): %v", err)
	}
	if v := r.CheckRegression(base, 0); len(v) > 0 {
		for _, viol := range v {
			t.Errorf("cassette regression: %s", viol)
		}
	}
}

// corpusRepeats is how many times the cassette corpus is replayed so the report
// has N≥30 turns (the Sprint-2 plan's stated sample floor for p50/p95/p99). The
// cassette/stub path is deterministic and sub-second, so replaying is free; the
// 3 cassette-complete clips yield 8 turns each pass, so 4 passes → 32 turns.
const corpusRepeats = 4

// runCassetteCorpus drives every cassette-complete corpus clip through the
// bench-owned Conversation (real silero VAD via NewVADRig, stub LLM+TTS),
// replaying the corpus corpusRepeats times to reach the N≥30 sample floor, and
// returns the aggregated cassette-tier report.
func runCassetteCorpus(t *testing.T) Report {
	t.Helper()
	acc := NewAccumulator("cassette", corpusTierNames())

	for pass := 0; pass < corpusRepeats; pass++ {
		for _, clip := range Corpus {
			if !clip.Cassette() {
				continue // live-tier-only clips (no full cassette set)
			}
			runClipThroughRig(t, clip, acc)
		}
	}
	return acc.Build()
}

// runClipThroughRig wires one clip's real silero VAD + stub providers and drives
// it through the Driver, folding its turn into acc.
func runClipThroughRig(t *testing.T, clip Clip, acc *Accumulator) {
	t.Helper()
	// Real silero VAD + the clip's frames, plus a fresh Harness bus.
	vadStage, h, frames := voicetest.NewVADRig(t, clip.Dir)

	// ONE tap per clip: the agenttool adapter records llm_round/provider_* onto
	// it, AND observe's StageSubscriber (installed in BuildConversation) records
	// the bus-derived headline stages onto the same instance. The Driver drains
	// it after Flush. The same instance must reach both seams or its samples
	// vanish — so it's threaded through RigConfig.Recorder AND NewDriver.
	tap := newRecorderTap()

	target := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}
	conv := BuildConversation(RigConfig{
		Bus:      h.Bus,
		VAD:      vadStage,
		STT:      orchestrator.NewSTT(h.Bus, stubRecognizer{text: "another ale, innkeeper"}),
		Persona:  agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: benchVoice()},
		Provider: stubProvider{}, // default 3-sentence reply → exercises B1 per-sentence dispatch
		Synth:    stubSynth{},
		Detector: orchestrator.NewAddressDetector(alwaysRoute{target: target}),
		Recorder: tap,
	})

	// One silence frame sized to the clip framing, appended to provoke speech-end.
	silence := silenceLike(t, frames)
	d := NewDriver(conv, h, tap, acc, silence, 20)
	if err := d.RunClip(context.Background(), frames); err != nil {
		t.Fatalf("clip %q: %v", clip.Dir, err)
	}
}

// silenceLike returns a zero-PCM frame matching the framing of sample[0].
func silenceLike(t *testing.T, sample []audio.Frame) audio.Frame {
	t.Helper()
	if len(sample) == 0 {
		t.Fatal("clip produced no frames")
	}
	f := sample[0]
	z, err := audio.NewFrame(make([]int16, len(f.Samples())), f.SampleRate(), f.FrameMs())
	if err != nil {
		t.Fatalf("silence frame: %v", err)
	}
	return z
}

func corpusTierNames() []string {
	seen := map[Tier]bool{}
	var out []string
	for _, c := range Corpus {
		if c.Cassette() && !seen[c.Tier] {
			seen[c.Tier] = true
			out = append(out, string(c.Tier))
		}
	}
	return out
}

// repoPath resolves a repo-relative path by walking up to the module root.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("module root (go.mod) not found from %s", rel)
	return ""
}
