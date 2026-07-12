//go:build live

// Live latency measurement for the deployment LLM (ADR-0036, #424 amendment):
// openai/gpt-oss-120b on Groq. Excluded from the default keyless suite by the
// `live` tag — it makes PAID calls to the real Groq endpoint and only runs with
// `go test -tags=live` and GROQ_API_KEY set (key from the keyring via env,
// never printed).
//
// ADR-0036 moved the default LLM off gemini-2.5-flash; the #424 amendment then
// moved it off llama-3.3-70b-versatile and onto openai/gpt-oss-120b on Groq's
// LPU after a live A/B (cleaner tool calls, natural German, cheaper). Unlike the
// prior llama default, gpt-oss-120b IS reasoning-capable, and the default path
// sends no reasoning_effort — so its reasoning tokens share the 1024
// DefaultMaxTokens budget. The #424 A/B accepted a higher first_audio
// (~1.9–3.5 s vs llama's ~1.4–2.0 s) as within budget. This test measures the
// LLM-stage half of that: it reports the wall-time DISTRIBUTION (p50/p95/p99) of
// time-to-first-content-token and total completion across two prompt tiers,
// against the ADR-0036 caveat to "measure the live tier from the deployment's
// egress before assuming" the research TTFT (a self-serve key may route US).
//
// Descendant of gemini's TestLive_ThinkingCap_AB: that test A/B'd three
// reasoning_effort arms to prove the cap tightened Gemini's tail. The two tiers
// here ARE the evidence in the same spirit — a trivial control vs a
// reasoning-bait prompt. On a reasoning model the bait tier MAY open a thinking
// stall before the first token; the per-tier readout is exactly what shows
// whether that tail stays inside the #424-accepted first_audio envelope on the
// real endpoint (and whether reasoning tokens starve the reply within the cap).
//
// This is an ISOLATED provider call (one-line system prompt, no history, no
// tools, no orchestrator), so it proves the LLM-STAGE TTFT, not the in-pipeline
// glyphoxa_voice_llm_round / response_latency — that absolute SLO is the
// bench-live tier's job (voicebench.TestBench_LiveSLO). Don't conflate the two.
package groq_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// ttftPrompts are the two discriminating tiers carried over from the Gemini A/B:
// a trivial reply (control — no reasoning) and a reasoning-bait prompt (the one
// that grew Gemini's thinking tail). The Bart system prompt keeps the model in
// character so the logged answers are quality-judgeable. This harness sends no
// tools, so a third (dice) prompt would just be a second bait — omitted.
var ttftPrompts = []struct {
	name   string
	system string
	user   string
}{
	{"trivial", "You are Bart, a gruff but warm tavern innkeeper. Reply in one short spoken line.", "Bart, noch ein Bier?"},
	{"reasoning-bait", "You are Bart, a gruff but warm tavern innkeeper. Reply in one short spoken line.", "Bart, if three travelers split a 17-copper tab evenly but one only drank half, what does each owe?"},
}

// TestLive_GroqGPTOSS_TTFT measures the deployment LLM's live wall-time
// distribution per prompt tier: time-to-first-content-token (the headline TTFT
// the SLO leans on) and total completion time. It drives the production default
// (groq.New("") ⇒ openai/gpt-oss-120b) against the real Groq endpoint.
//
// Pacing:
//   - a per-call sleep (GX_GROQ_DELAY) keeps the loop under Groq's per-model RPM
//     throttle; the default is modest because the LPU's free tier is far more
//     generous than Gemini's old 5-req/min ceiling.
//
// Reporting is bucketed PER tier: pooling trivial with reasoning-bait would hide
// the very comparison the test exists to make — how far does the bait tier's
// reasoning stall push TTFT past the trivial tier on gpt-oss-120b? Set
// GX_GROQ_LOG_ALL=1 to log every answer for a German-quality eyeball (an
// ADR-0036 caveat).
//
// The test asserts only that some sample landed; the verdict is the printed
// per-tier distribution (fold into ADR-0036's "re-test before launch" caveats).
func TestLive_GroqGPTOSS_TTFT(t *testing.T) {
	if os.Getenv(groq.APIKeyEnv) == "" {
		t.Skipf("%s not set; skipping paid live Groq latency run", groq.APIKeyEnv)
	}
	n := 8 // per tier; small — this is the shared deployment key.
	if v := os.Getenv("GX_GROQ_N"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	delay := 2 * time.Second // Groq's RPM is generous; a light delay still smooths bursts.
	if v := os.Getenv("GX_GROQ_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			delay = d
		}
	}
	// GX_GROQ_LOG_ALL=1 logs EVERY answer (not just the first per tier) so a
	// quality re-run can eyeball the full set — ADR-0036 flags German as unproven
	// on real NPC prompts, and one sample per tier can't show that.
	logAll := os.Getenv("GX_GROQ_LOG_ALL") == "1"

	// The production default: empty key ⇒ GROQ_API_KEY, default model ⇒
	// openai/gpt-oss-120b, no reasoning_effort sent (reasoning tokens, if any,
	// share the DefaultMaxTokens budget).
	client := groq.New("")

	// samples bucketed by tier name → the per-tier distribution; failed calls
	// (provider EventError, truncation, call never started) are counted per
	// kind instead of polluting the latency samples (#155).
	ttft := map[string][]float64{}
	total := map[string][]float64{}
	fails := map[string]map[string]int{}

	var calls int
	for _, p := range ttftPrompts {
		for i := 0; i < n; i++ {
			if calls > 0 {
				time.Sleep(delay)
			}
			calls++
			first, tot, text, err := timeOne(client, p.system, p.user)
			if err != nil {
				t.Logf("[%s #%d] FAILED (%s): %v", p.name, i, failureKind(err), err)
				if fails[p.name] == nil {
					fails[p.name] = map[string]int{}
				}
				fails[p.name][failureKind(err)]++
				continue
			}
			ttft[p.name] = append(ttft[p.name], first)
			total[p.name] = append(total[p.name], tot)
			if logAll || i == 0 { // answer(s) for the quality check.
				t.Logf("[%s #%d] %q", p.name, i, trim(text, 200))
			}
		}
	}

	var any bool
	for _, p := range ttftPrompts {
		t.Logf("[%s] ttft_ms  %s", p.name, dist(ttft[p.name]))
		t.Logf("[%s] total_ms %s", p.name, dist(total[p.name]))
		t.Logf("[%s] failed   %s", p.name, failSummary(fails[p.name]))
		any = any || len(ttft[p.name]) > 0
	}
	if !any {
		t.Fatal("no successful samples on any tier — check key/quota (and the per-kind failure counts above)")
	}
}

// timeOne runs one completion, returning ms-to-first-content-token, ms-total,
// and the accumulated text. Tools are intentionally omitted: this isolates the
// LLM-stage TTFT from tool-loop rounds (ADR-0028); the bait prompt still
// exercises a reasoning-shaped input without adding a second round-trip.
//
// Drain/classification lives in drainStream (latency_drain_test.go, no live
// tag) so the failure modes — mid-stream EventError, truncation — are pinned
// by keyless unit tests (#155). A non-nil err means the sample must NOT be
// recorded: only streams that delivered a terminal completion count.
func timeOne(c *groq.Client, system, user string) (firstMs, totalMs float64, text string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	ch, err := c.Complete(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: system},
			{Role: llm.RoleUser, Text: user},
		},
	})
	if err != nil {
		return 0, 0, "", err
	}
	first, tot, text, err := drainStream(start, ch)
	return ms(first), ms(tot), text, err
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// dist formats p50/p95/p99/max over a sample — the tail is the point (we report
// the distribution, not the mean: TTFT consistency is the LPU's selling point).
func dist(xs []float64) string {
	if len(xs) == 0 {
		return "(no samples)"
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	pct := func(p float64) float64 {
		idx := int(p * float64(len(s)-1))
		return s[idx]
	}
	return fmt.Sprintf("n=%d p50=%.0f p95=%.0f p99=%.0f max=%.0f",
		len(s), pct(0.50), pct(0.95), pct(0.99), s[len(s)-1])
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
