package llmbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

func TestPercentile_NearestRank(t *testing.T) {
	vals := []float64{50, 10, 40, 20, 30, 60, 70, 80, 90, 100}
	if got := percentile(vals, 50); got != 50 {
		t.Errorf("p50 = %v, want 50", got)
	}
	if got := percentile(vals, 95); got != 100 {
		t.Errorf("p95 = %v, want 100", got)
	}
	if got := percentile(nil, 50); got != -1 {
		t.Errorf("p50 of nothing = %v, want -1", got)
	}
	if vals[0] != 50 {
		t.Error("percentile sorted its input in place")
	}
}

func sampleTurns() []TurnResult {
	return []TurnResult{
		{Script: "s", Index: 0, Speaker: "Mira", User: "Hallo", Reply: "Na, meine Liebe.", Tags: []string{TagSmallTalk},
			FirstDeltaMS: 100, FirstSentenceMS: 200, EndMS: 300, Rounds: 1, ToolsOK: true,
			InputTokens: 1000, OutputTokens: 100, CostUSD: 0.001},
		{Script: "s", Index: 1, Speaker: "Brak", User: "Wirf", Reply: "Eine 17.", Tags: []string{TagDiceSingle},
			FirstDeltaMS: 300, FirstSentenceMS: 400, EndMS: 500, Rounds: 2, ToolsOK: true,
			ToolCalls: []ToolCall{{Name: "dice", SchemaValid: true}}, Expect: []string{"dice"},
			InputTokens: 2000, OutputTokens: 100, CostUSD: 0.002,
			RawFindings: Findings{StageDirections: 1}},
		{Script: "s", Index: 2, Speaker: "Mira", User: "Merk dir", Reply: "Mach ich, mein Lieber.", Tags: []string{TagRememberAccept},
			FirstDeltaMS: -1, FirstSentenceMS: 600, EndMS: 700, Rounds: 1, ToolsOK: false,
			ToolCalls: []ToolCall{{Name: "kg_query", SchemaValid: false, SchemaError: "x"}}, Expect: []string{"remember_knowledge"},
			InputTokens: 1000, OutputTokens: 100, CostUSD: 0.003,
			SpokenFindings: Findings{GenderSlip: 1}, Malformed: map[string]int{"text_leak": 2}},
		{Script: "s", Index: 3, Speaker: "Brak", User: "Hm", Reply: "Hm... einen Moment, ich habe gerade den Faden verloren.", Tags: []string{TagSmallTalk},
			FirstDeltaMS: -1, FirstSentenceMS: -1, EndMS: 900, ToolsOK: true, Fallback: true, Error: "boom"},
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize(sampleTurns())

	if s.Turns != 4 || s.Errors != 1 {
		t.Errorf("Turns/Errors = %d/%d, want 4/1", s.Turns, s.Errors)
	}
	// Timings skip the turns where the event never happened (-1).
	if s.FirstDelta.N != 2 || s.FirstDelta.P50 != 100 || s.FirstDelta.P95 != 300 {
		t.Errorf("FirstDelta = %+v, want n=2 p50=100 p95=300", s.FirstDelta)
	}
	if s.FirstSentence.N != 3 || s.FirstSentence.P50 != 400 {
		t.Errorf("FirstSentence = %+v, want n=3 p50=400", s.FirstSentence)
	}
	if s.End.N != 4 || s.End.P95 != 900 {
		t.Errorf("End = %+v, want n=4 p95=900", s.End)
	}
	if s.ToolsOKPct != 75 {
		t.Errorf("ToolsOKPct = %v, want 75 (3 of 4 turns)", s.ToolsOKPct)
	}
	if s.ToolCalls != 2 || s.SchemaValidPct != 50 {
		t.Errorf("ToolCalls/SchemaValidPct = %d/%v, want 2/50", s.ToolCalls, s.SchemaValidPct)
	}
	if s.Spoken.GenderSlip != 1 || s.Raw.StageDirections != 1 {
		t.Errorf("findings = spoken %+v raw %+v", s.Spoken, s.Raw)
	}
	if s.Malformed["text_leak"] != 2 {
		t.Errorf("Malformed = %v, want text_leak 2", s.Malformed)
	}
	if want := 0.006 / 4 * 100; s.CostPer100TurnsUSD < want*0.999 || s.CostPer100TurnsUSD > want*1.001 {
		t.Errorf("CostPer100TurnsUSD = %v, want %v", s.CostPer100TurnsUSD, want)
	}
	if s.InputTokens != 4000 || s.OutputTokens != 300 || s.Rounds != 4 {
		t.Errorf("tokens/rounds = %d/%d/%d", s.InputTokens, s.OutputTokens, s.Rounds)
	}
}

func sampleReport() CandidateReport {
	turns := sampleTurns()
	return CandidateReport{
		Candidate: CandidateInfo{Name: "fake-arm", Provider: string(observe.ProviderGroq), Model: "m", Settings: "x"},
		Turns:     turns, Summary: Summarize(turns),
	}
}

func TestMarkdownTable_OneRowPerCandidate(t *testing.T) {
	md := MarkdownTable([]CandidateReport{sampleReport()})
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) != 3 {
		t.Fatalf("table has %d lines, want header + rule + 1 row:\n%s", len(lines), md)
	}
	for _, want := range []string{"first sentence p50 / p95", "tools OK", "$ / 100 turns"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header lacks %q: %s", want, lines[0])
		}
	}
	for _, want := range []string{"fake-arm", "100 / 300", "400 / 600", "75%", "50%", "$0.150"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("row lacks %q: %s", want, lines[2])
		}
	}
	if strings.Count(lines[0], "|") != strings.Count(lines[2], "|") {
		t.Errorf("header and row have different column counts:\n%s", md)
	}
}

func TestReplyDump_ListsEveryReplyWithItsFlags(t *testing.T) {
	dump := ReplyDump(sampleReport())
	for _, want := range []string{"fake-arm", "Mira: Hallo", "Na, meine Liebe.", "gender_slip", "FALLBACK", "kg_query", "expected remember_knowledge"} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump lacks %q:\n%s", want, dump)
		}
	}
}

func TestWriteReports_OneJSONAndOneDumpPerCandidatePlusTheTable(t *testing.T) {
	dir := t.TempDir()
	if err := WriteReports(dir, []CandidateReport{sampleReport()}); err != nil {
		t.Fatalf("WriteReports: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "fake-arm.json"))
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var back CandidateReport
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Turns) != 4 || back.Summary.Turns != 4 {
		t.Errorf("json report does not round-trip: err=%v turns=%d", err, len(back.Turns))
	}
	for _, name := range []string{"fake-arm.replies.md", "comparison.md"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || len(b) == 0 {
			t.Errorf("%s: missing or empty (err %v)", name, err)
		}
	}
}
