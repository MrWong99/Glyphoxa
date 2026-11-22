package llmbench

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// CandidateInfo is the part of a [Candidate] that goes into a report.
type CandidateInfo struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Settings string `json:"settings"`
}

func info(c Candidate) CandidateInfo {
	return CandidateInfo{Name: c.Name, Provider: string(c.Provider), Model: c.Model, Settings: c.Settings}
}

// CandidateReport is one candidate's full result: every turn and the summary.
type CandidateReport struct {
	Candidate CandidateInfo `json:"candidate"`
	Summary   Summary       `json:"summary"`
	Turns     []TurnResult  `json:"turns"`
}

// Dist is a latency distribution in milliseconds over the N turns that have
// the measurement. P50/P95 are nearest-rank percentiles, -1 when N is 0.
type Dist struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	Max float64 `json:"max_ms"`
}

// Summary reduces a candidate's turns to the comparison figures.
type Summary struct {
	Turns  int `json:"turns"`
	Errors int `json:"errors"`

	FirstDelta    Dist `json:"first_delta"`
	FirstSentence Dist `json:"first_sentence"`
	End           Dist `json:"end"`
	// FirstSentenceOneRound is the first-sentence latency over the turns that
	// took a single completion (plain conversation); FirstSentenceToolTurns
	// over the turns that took more (at least one tool round came first).
	FirstSentenceOneRound  Dist `json:"first_sentence_one_round"`
	FirstSentenceToolTurns Dist `json:"first_sentence_tool_turns"`

	// ToolsOKPct is the share of turns whose tool use was right: every expected
	// Tool called, nothing unexpected (a no-tool turn must call nothing).
	ToolsOKPct float64 `json:"tools_ok_pct"`
	// ToolTurnsOKPct is the same over the turns that expect a Tool only.
	ToolTurnsOKPct float64 `json:"tool_turns_ok_pct"`
	ToolCalls      int     `json:"tool_calls"`
	// SchemaValidPct is the share of tool calls with schema-valid arguments;
	// 100 when no call was made.
	SchemaValidPct float64 `json:"schema_valid_pct"`
	ToolErrors     int     `json:"tool_errors"`
	Rounds         int     `json:"rounds"`
	// PreToolTurns counts turns in which the model wrote prose in the same
	// round as a tool call — text that is spoken before the Tool has run.
	PreToolTurns int `json:"pre_tool_turns"`
	// Silent counts turns that failed with no error and nothing to speak.
	Silent int `json:"silent"`

	Spoken    Findings       `json:"spoken_findings"`
	Raw       Findings       `json:"raw_findings"`
	Malformed map[string]int `json:"malformed,omitempty"`

	InputTokens        int     `json:"input_tokens"`
	OutputTokens       int     `json:"output_tokens"`
	CostUSD            float64 `json:"cost_usd"`
	CostPer100TurnsUSD float64 `json:"cost_per_100_turns_usd"`
}

// percentile is the nearest-rank p-th percentile of vals, -1 for no values.
// vals is not modified.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return -1
	}
	sorted := slices.Clone(vals)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func dist(vals []float64) Dist {
	d := Dist{N: len(vals), P50: percentile(vals, 50), P95: percentile(vals, 95), Max: -1}
	if len(vals) > 0 {
		d.Max = slices.Max(vals)
	}
	return d
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 100
	}
	return math.Round(float64(part)/float64(whole)*1000) / 10
}

// Summarize reduces turns to a [Summary].
func Summarize(turns []TurnResult) Summary {
	s := Summary{Turns: len(turns), Malformed: map[string]int{}}
	var delta, sentence, end, oneRound, toolTurn []float64
	ok, toolTurns, toolTurnsOK, valid := 0, 0, 0, 0
	for _, t := range turns {
		if t.Error != "" {
			s.Errors++
		}
		if t.FirstDeltaMS >= 0 {
			delta = append(delta, t.FirstDeltaMS)
		}
		if t.FirstSentenceMS >= 0 {
			sentence = append(sentence, t.FirstSentenceMS)
			if t.Rounds > 1 {
				toolTurn = append(toolTurn, t.FirstSentenceMS)
			} else {
				oneRound = append(oneRound, t.FirstSentenceMS)
			}
		}
		end = append(end, t.EndMS)
		if t.ToolsOK {
			ok++
		}
		if len(t.Expect) > 0 {
			toolTurns++
			if t.ToolsOK {
				toolTurnsOK++
			}
		}
		for _, c := range t.ToolCalls {
			s.ToolCalls++
			if c.SchemaValid {
				valid++
			}
		}
		s.ToolErrors += t.ToolErrors
		if t.PreToolText != "" {
			s.PreToolTurns++
		}
		if t.Reply == "" && t.Error == "" {
			s.Silent++
		}
		s.Rounds += t.Rounds
		s.Spoken.Add(t.SpokenFindings)
		s.Raw.Add(t.RawFindings)
		for k, v := range t.Malformed {
			s.Malformed[k] += v
		}
		s.InputTokens += t.InputTokens
		s.OutputTokens += t.OutputTokens
		s.CostUSD += t.CostUSD
	}
	s.FirstDelta, s.FirstSentence, s.End = dist(delta), dist(sentence), dist(end)
	s.FirstSentenceOneRound, s.FirstSentenceToolTurns = dist(oneRound), dist(toolTurn)
	s.ToolsOKPct = pct(ok, len(turns))
	s.ToolTurnsOKPct = pct(toolTurnsOK, toolTurns)
	s.SchemaValidPct = pct(valid, s.ToolCalls)
	if len(turns) > 0 {
		s.CostPer100TurnsUSD = s.CostUSD / float64(len(turns)) * 100
	}
	if len(s.Malformed) == 0 {
		s.Malformed = nil
	}
	return s
}

func pair(d Dist) string {
	if d.N == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f / %.0f", d.P50, d.P95)
}

// leak renders a finding as "spoken (raw)": what would reach the voice, and in
// parentheses what the model wrote before the production scrub.
func leak(spoken, raw int) string { return fmt.Sprintf("%d (%d)", spoken, raw) }

// signals renders the production path's malformed-generation counters, e.g.
// "3 (roll_claim×2, text_leak×1)".
func signals(m map[string]int) string {
	if len(m) == 0 {
		return "0"
	}
	keys := make([]string, 0, len(m))
	total := 0
	for k, v := range m {
		keys = append(keys, k)
		total += v
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s×%d", k, m[k])
	}
	return fmt.Sprintf("%d (%s)", total, strings.Join(parts, ", "))
}

// MarkdownTable renders the comparison as two tables, one row per candidate
// each: latency and cost, then behaviour. Timings are milliseconds from the
// start of the turn. Detector findings are "spoken (raw)".
func MarkdownTable(reports []CandidateReport) string {
	var b strings.Builder
	b.WriteString("| candidate | turns | errors | first delta p50 / p95 | first sentence p50 / p95 | first sentence, one-round turns | first sentence, tool turns | end p50 / p95 | rounds / turn | $ / 100 turns |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range reports {
		s := r.Summary
		rounds := 0.0
		if s.Turns > 0 {
			rounds = float64(s.Rounds) / float64(s.Turns)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %s | %s | %.2f | $%.3f |\n",
			r.Candidate.Name, s.Turns, s.Errors,
			pair(s.FirstDelta), pair(s.FirstSentence), pair(s.FirstSentenceOneRound), pair(s.FirstSentenceToolTurns), pair(s.End),
			rounds, s.CostPer100TurnsUSD)
	}

	b.WriteString("\n| candidate | tools OK | tool turns OK | schema-valid calls | prose before tool | silent | guard signals | tool markup | stage directions | markdown | English | tool narration | gender slips |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range reports {
		s := r.Summary
		fmt.Fprintf(&b, "| %s | %.0f%% | %.0f%% | %.0f%% (%d) | %d | %d | %s | %s | %s | %s | %s | %s | %s |\n",
			r.Candidate.Name,
			s.ToolsOKPct, s.ToolTurnsOKPct, s.SchemaValidPct, s.ToolCalls, s.PreToolTurns, s.Silent, signals(s.Malformed),
			leak(s.Spoken.ToolMarkup, s.Raw.ToolMarkup),
			leak(s.Spoken.StageDirections, s.Raw.StageDirections),
			leak(s.Spoken.Markdown, s.Raw.Markdown),
			leak(s.Spoken.English, s.Raw.English),
			leak(s.Spoken.ToolNarration, s.Raw.ToolNarration),
			leak(s.Spoken.GenderSlip, s.Raw.GenderSlip))
	}
	return b.String()
}

// flags lists the non-zero findings of f by name.
func flags(f Findings) []string {
	var out []string
	add := func(name string, n int) {
		if n > 0 {
			out = append(out, fmt.Sprintf("%s×%d", name, n))
		}
	}
	add("tool_markup", f.ToolMarkup)
	add("stage_directions", f.StageDirections)
	add("markdown", f.Markdown)
	add("english", f.English)
	add("tool_narration", f.ToolNarration)
	add("gender_slip", f.GenderSlip)
	return out
}

// ReplyDump renders every reply of one candidate for a human read: the player
// line, what would be spoken, the tool calls, and whatever the detectors or the
// tool check flagged. The raw model text is shown only where it differs from
// what was spoken.
func ReplyDump(r CandidateReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s / %s (%s)\n", r.Candidate.Name, r.Candidate.Provider, r.Candidate.Model, r.Candidate.Settings)
	script := ""
	for _, t := range r.Turns {
		if t.Script != script {
			script = t.Script
			fmt.Fprintf(&b, "\n## %s\n", script)
		}
		fmt.Fprintf(&b, "\n**%d. %s: %s**  \n", t.Index+1, t.Speaker, t.User)
		reply := t.Reply
		if reply == "" {
			reply = "(nothing spoken)"
		}
		fmt.Fprintf(&b, "> %s\n\n", reply)

		var notes []string
		notes = append(notes, fmt.Sprintf("%s; first sentence %.0f ms, end %.0f ms, %d round(s)", strings.Join(t.Tags, ", "), t.FirstSentenceMS, t.EndMS, t.Rounds))
		for _, c := range t.ToolCalls {
			note := fmt.Sprintf("called %s %s", c.Name, c.Input)
			if !c.SchemaValid {
				note += " — INVALID: " + c.SchemaError
			}
			notes = append(notes, note)
		}
		if !t.ToolsOK {
			want := "no tool"
			if len(t.Expect) > 0 {
				want = strings.Join(t.Expect, ", ")
			}
			notes = append(notes, "TOOL CHECK FAILED: expected "+want)
		}
		if t.PreToolText != "" {
			notes = append(notes, "prose before the tool call: "+t.PreToolText)
		}
		if f := flags(t.SpokenFindings); len(f) > 0 {
			notes = append(notes, "spoken: "+strings.Join(f, ", "))
		}
		if f := flags(t.RawFindings); len(f) > 0 {
			notes = append(notes, "raw: "+strings.Join(f, ", "))
		}
		if t.Fallback {
			notes = append(notes, "FALLBACK line spoken")
		}
		if t.Error != "" {
			notes = append(notes, "error: "+t.Error)
		}
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		if raw := strings.Join(strings.Fields(t.Raw), " "); raw != "" && raw != strings.Join(strings.Fields(t.Reply), " ") {
			fmt.Fprintf(&b, "- raw model text: `%s`\n", strings.ReplaceAll(raw, "`", "'"))
		}
	}
	return b.String()
}

// WriteReports writes, into dir: <candidate>.json (the full report) and
// <candidate>.replies.md (the reply dump) per candidate, and comparison.md.
func WriteReports(dir string, reports []CandidateReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, r := range reports {
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, r.Candidate.Name+".json"), raw, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, r.Candidate.Name+".replies.md"), []byte(ReplyDump(r)), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "comparison.md"), []byte(MarkdownTable(reports)), 0o644)
}
