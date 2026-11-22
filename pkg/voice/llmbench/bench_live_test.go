//go:build bench && live

package llmbench

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLLMBench_Live runs the corpus against the selected candidates on the
// live provider APIs and writes the reports. It is a measurement, not a gate:
// it fails only when the run itself is broken (a missing credential, a
// candidate that never produced a reply).
//
//	GX_LLMBENCH_CANDIDATES  comma-separated candidate names; "" = the default
//	                        arms, "all" = every arm including the optional ones
//	GX_LLMBENCH_SCRIPTS     comma-separated script names; "" = all
//	GX_LLMBENCH_REPEAT      passes over the corpus (default 1)
//	GX_LLMBENCH_DELAY       pause before every turn (default 400ms)
//	GX_LLMBENCH_OUT         output directory (default: a temp dir, logged)
func TestLLMBench_Live(t *testing.T) {
	selected, err := SelectCandidates(Candidates(), os.Getenv("GX_LLMBENCH_CANDIDATES"))
	if err != nil {
		t.Fatal(err)
	}
	if missing := MissingKeys(selected); len(missing) > 0 {
		t.Fatalf("llmbench: missing credentials: %s", strings.Join(missing, ", "))
	}

	scripts := Scripts()
	if want := os.Getenv("GX_LLMBENCH_SCRIPTS"); want != "" {
		var picked []Script
		for _, name := range strings.Split(want, ",") {
			found := false
			for _, s := range scripts {
				if s.Name == strings.TrimSpace(name) {
					picked, found = append(picked, s), true
				}
			}
			if !found {
				t.Fatalf("llmbench: unknown script %q", name)
			}
		}
		scripts = picked
	}
	if n, _ := strconv.Atoi(os.Getenv("GX_LLMBENCH_REPEAT")); n > 1 {
		base := scripts
		for pass := 2; pass <= n; pass++ {
			for _, s := range base {
				scripts = append(scripts, Script{Name: fmt.Sprintf("%s#%d", s.Name, pass), Turns: s.Turns})
			}
		}
	}

	delay := 400 * time.Millisecond
	if v := os.Getenv("GX_LLMBENCH_DELAY"); v != "" {
		if delay, err = time.ParseDuration(v); err != nil {
			t.Fatalf("llmbench: GX_LLMBENCH_DELAY: %v", err)
		}
	}
	out := os.Getenv("GX_LLMBENCH_OUT")
	if out == "" {
		out = t.TempDir()
	}

	reports := Runner{Candidates: selected, Scripts: scripts, Delay: delay, Logf: t.Logf}.Run(t.Context())

	if err := WriteReports(out, reports); err != nil {
		t.Fatalf("llmbench: write reports: %v", err)
	}
	t.Logf("reports written to %s\n\n%s", out, MarkdownTable(reports))

	for _, r := range reports {
		if r.Summary.Turns > 0 && r.Summary.Errors == r.Summary.Turns {
			t.Errorf("candidate %s: every turn failed (first error: %s)", r.Candidate.Name, r.Turns[0].Error)
		}
	}
}
