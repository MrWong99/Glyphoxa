//go:build bench

package llmbench

import (
	"os"
	"testing"
)

// TestLLMBench_Rescore re-scores a finished run with the current detectors and
// corpus expectations — no provider is called. It reads the <candidate>.json
// reports in GX_LLMBENCH_RESCORE and writes the re-scored reports, reply dumps
// and comparison table to GX_LLMBENCH_OUT (default: a temp dir, logged). It is
// skipped when GX_LLMBENCH_RESCORE is not set.
func TestLLMBench_Rescore(t *testing.T) {
	in := os.Getenv("GX_LLMBENCH_RESCORE")
	if in == "" {
		t.Skip("GX_LLMBENCH_RESCORE not set")
	}
	reports, err := ReadReports(in)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range reports {
		reports[i] = Rescore(r, Scripts())
	}
	out := os.Getenv("GX_LLMBENCH_OUT")
	if out == "" {
		out = t.TempDir()
	}
	if err := WriteReports(out, reports); err != nil {
		t.Fatalf("llmbench: write reports: %v", err)
	}
	t.Logf("re-scored reports written to %s\n\n%s", out, MarkdownTable(reports))
}
