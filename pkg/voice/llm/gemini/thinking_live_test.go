//go:build live

// Live A/B for the B2 thinking cap (ADR-0035). Excluded from the default keyless
// suite by the `live` tag — it makes PAID calls to the real Gemini endpoint and
// only runs with `go test -tags=live` and GEMINI_API_KEY set (key from the
// keyring via env, never printed). It measures whether reasoning_effort:"low"
// actually tightens the wall-time DISTRIBUTION (p50/p95) vs. the uncapped
// default — the claim keyless tests cannot close, since a silently-ignored field
// would pass them.
package gemini_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
)

// abPrompts is trimmed to the two discriminating tiers: a trivial reply
// (control — little thinking) and a reasoning-bait prompt (where H1's thinking
// tail lives — the one that proves whether reasoning_effort:"low" is honored on
// 2.5-flash and cuts wall-time). Dice is dropped: this harness sends no tools,
// so it would just be a third bait prompt, and H2 (tool rounds) is not what B2
// is about. System prompt keeps Bart in character so quality is judgeable.
var abPrompts = []struct {
	name   string
	system string
	user   string
}{
	{"trivial", "You are Bart, a gruff but warm tavern innkeeper. Reply in one short spoken line.", "Bart, noch ein Bier?"},
	{"reasoning-bait", "You are Bart, a gruff but warm tavern innkeeper. Reply in one short spoken line.", "Bart, if three travelers split a 17-copper tab evenly but one only drank half, what does each owe?"},
}

// TestLive_ThinkingCap_AB runs an INTERLEAVED A/B of the uncapped default vs.
// reasoning_effort:"low" and prints the wall-time distribution: time-to-first-
// content-token (the cleanest H1 / thinking signal) and total completion time.
//
// Pacing & interleaving matter on the free-tier key (5 req/min RPM throttle):
//   - a per-call sleep (GX_AB_DELAY, default 13s) keeps us under the RPM limit;
//   - arms alternate per iteration so neither eats a whole minute's quota and
//     so server-load drift is shared, not confounded into one arm.
//
// Reporting is bucketed PER (arm, prompt): trivial never triggers thinking, so
// pooling it with reasoning-bait would dilute the very effect B2 measures. The
// bait tier carries the latency story (does low/medium cut the tail?) and the
// quality check (set GX_AB_LOG_ALL=1 to log every answer and eyeball it). The
// three arms — uncapped default, low, medium — let the chosen default be picked
// from evidence: if "low" muddles hard reasoning, "medium" is the next notch
// before abandoning the cap.
//
// The test asserts only that some arm produced samples; the verdict is the
// printed per-(arm,prompt) distributions (fold into ADR-0035).
func TestLive_ThinkingCap_AB(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set; skipping paid live A/B")
	}
	n := 8 // per (arm,prompt); small — this is the shared deployment key.
	if v := os.Getenv("GX_AB_N"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	delay := 13 * time.Second // stay under the 5-req/min free-tier RPM throttle.
	if v := os.Getenv("GX_AB_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			delay = d
		}
	}
	// GX_AB_LOG_ALL=1 logs EVERY answer (not just the first per arm/prompt) so a
	// quality re-run can eyeball the full set — capping thinking can muddle the
	// reasoning-bait answers, and one sample per arm can't show that.
	logAll := os.Getenv("GX_AB_LOG_ALL") == "1"

	type arm struct {
		name   string
		client *gemini.Client
		// samples bucketed by prompt name → the per-tier distribution.
		ttft  map[string][]float64
		total map[string][]float64
	}
	newArm := func(name, effort string) *arm {
		return &arm{
			name:   name,
			client: gemini.New("", gemini.WithReasoningEffort(effort)),
			ttft:   map[string][]float64{},
			total:  map[string][]float64{},
		}
	}
	arms := []*arm{
		newArm("default-uncapped", ""),
		newArm("effort-low", "low"),
		newArm("effort-medium", "medium"),
	}

	var calls int
	for _, p := range abPrompts {
		for i := 0; i < n; i++ {
			for _, a := range arms { // interleave arms so server drift is shared.
				if calls > 0 {
					time.Sleep(delay)
				}
				calls++
				first, tot, text, err := timeOne(t, a.client, p.system, p.user)
				if err != nil {
					t.Logf("[%s/%s #%d] %v", a.name, p.name, i, err)
					continue
				}
				a.ttft[p.name] = append(a.ttft[p.name], first)
				a.total[p.name] = append(a.total[p.name], tot)
				if logAll || i == 0 { // answer(s) for the quality check.
					t.Logf("[%s/%s #%d] %q", a.name, p.name, i, trim(text, 200))
				}
			}
		}
	}

	var any bool
	for _, p := range abPrompts { // report per tier, tier-major for easy A/B read.
		for _, a := range arms {
			t.Logf("[%s] ARM %-16s ttft_ms  %s", p.name, a.name, dist(a.ttft[p.name]))
			t.Logf("[%s] ARM %-16s total_ms %s", p.name, a.name, dist(a.total[p.name]))
			any = any || len(a.ttft[p.name]) > 0
		}
	}
	if !any {
		t.Fatal("no successful samples on any arm — check key/quota")
	}
}

// timeOne runs one completion, returning ms-to-first-content-token, ms-total,
// and the accumulated text. Tools are intentionally omitted: this isolates H1
// (thinking wall-time) from H2 (tool-loop rounds); the dice prompt still
// exercises a reasoning-shaped input without adding a second round-trip.
func timeOne(t *testing.T, c *gemini.Client, system, user string) (firstMs, totalMs float64, text string, err error) {
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
	var first time.Duration
	var sawFirst bool
	for ev := range ch {
		if ev.Type == llm.EventText && ev.Text != "" {
			if !sawFirst {
				first = time.Since(start)
				sawFirst = true
			}
			text += ev.Text
		}
	}
	tot := time.Since(start)
	if !sawFirst {
		first = tot
	}
	return ms(first), ms(tot), text, nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// dist formats p50/p95/p99/max over a sample — the tail is the point (we report
// the distribution, not the mean, per the plan).
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
