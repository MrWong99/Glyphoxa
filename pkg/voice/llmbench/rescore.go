package llmbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// detect fills the detector findings of res from its stored Reply and Raw. A
// fallback line is not the model's words, so it has no spoken findings.
func (res *TurnResult) detect() {
	opts := DetectOptions{NPCName: npcName, FemaleSpeaker: slices.Contains(res.Tags, TagFemalePC)}
	res.SpokenFindings = Findings{}
	if !res.Fallback {
		res.SpokenFindings = Detect(res.Reply, opts)
	}
	res.RawFindings = Detect(res.Raw, opts)
}

// Rescore re-applies the detectors and the corpus's tool expectations to a
// finished run. A report keeps every reply and every tool call, so a detector
// that was added or corrected afterwards, or a changed Expect/Allow, can be
// applied without calling a provider again. What was measured — timings,
// rounds, tokens, cost, the production guards' signals — stays as recorded.
//
// A turn is matched to scripts by script name (a repeat pass "name#2" is the
// script "name") and index; a turn the corpus no longer has keeps its recorded
// tool check. r is not modified.
func Rescore(r CandidateReport, scripts []Script) CandidateReport {
	byName := map[string]Script{}
	for _, s := range scripts {
		byName[s.Name] = s
	}
	out := CandidateReport{Candidate: r.Candidate, Turns: slices.Clone(r.Turns)}
	for i := range out.Turns {
		res := &out.Turns[i]
		res.detect()
		name, _, _ := strings.Cut(res.Script, "#")
		if s, ok := byName[name]; ok && res.Index >= 0 && res.Index < len(s.Turns) {
			turn := s.Turns[res.Index]
			res.Expect, res.Allow = turn.Expect, turn.Allow
			res.ToolsOK = turn.ToolsOK(toolNames(res.ToolCalls))
		}
	}
	out.Summary = Summarize(out.Turns)
	return out
}

// ReadReports reads the <candidate>.json reports [WriteReports] wrote into dir,
// in candidate-table order (a candidate the table no longer has comes last, by
// name). A directory without any report is an error.
func ReadReports(dir string) ([]CandidateReport, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var reports []CandidateReport
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var r CandidateReport
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("llmbench: %s: %w", p, err)
		}
		reports = append(reports, r)
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("llmbench: no <candidate>.json report in %s", dir)
	}
	rank := map[string]int{}
	for i, c := range Candidates() {
		rank[c.Name] = i + 1
	}
	sort.SliceStable(reports, func(i, j int) bool {
		a, b := rank[reports[i].Candidate.Name], rank[reports[j].Candidate.Name]
		switch {
		case a != 0 && b != 0:
			return a < b
		case a != 0 || b != 0:
			return a != 0
		}
		return reports[i].Candidate.Name < reports[j].Candidate.Name
	})
	return reports, nil
}
