package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeTranscript is a scripted TranscriptSearcher: it records the query/limit it
// was handed and replays a fixed hit set, so the handler's clamping and the
// campaign-inside-adapter contract are pinned without a DB.
type fakeTranscript struct {
	hits      []TranscriptHit
	err       error
	gotQuery  string
	gotLimit  int
	callCount int
}

func (f *fakeTranscript) SearchTranscript(_ context.Context, query string, limit int) ([]TranscriptHit, error) {
	f.callCount++
	f.gotQuery = query
	f.gotLimit = limit
	return f.hits, f.err
}

func TestTranscriptSearchRendersNumberedLines(t *testing.T) {
	at := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeTranscript{hits: []TranscriptHit{
		{Who: "Bart", Kind: "npc", Text: "I will remember your promise.", At: at},
		{Who: "GM", Kind: "human", Text: "You owe me a favour.", At: at},
	}}
	tool := NewTranscriptSearch(src)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"promise"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "1. [npc] Bart: I will remember your promise.\n2. [human] GM: You owe me a favour."
	if out != want {
		t.Errorf("render =\n%q\nwant\n%q", out, want)
	}
}

func TestTranscriptSearchDefaultsAndClampsLimit(t *testing.T) {
	src := &fakeTranscript{}
	tool := NewTranscriptSearch(src)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x"}`), nil); err != nil {
		t.Fatal(err)
	}
	if src.gotLimit != DefaultSearchLimit {
		t.Errorf("omitted limit → %d, want default %d", src.gotLimit, DefaultSearchLimit)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","limit":999}`), nil); err != nil {
		t.Fatal(err)
	}
	if src.gotLimit != MaxSearchLimit {
		t.Errorf("oversized limit → %d, want clamp to %d", src.gotLimit, MaxSearchLimit)
	}
}

func TestTranscriptSearchTruncatesLongLine(t *testing.T) {
	long := strings.Repeat("word ", 200) // ~1000 runes
	src := &fakeTranscript{hits: []TranscriptHit{{Who: "Bart", Kind: "npc", Text: long}}}
	out, err := NewTranscriptSearch(src).Execute(context.Background(), json.RawMessage(`{"query":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("a truncated line should end in an ellipsis: %q", out)
	}
	if len([]rune(out)) > MaxTranscriptLineRunes+40 { // + the "1. [npc] Bart: " prefix
		t.Errorf("line not truncated to ~%d runes: %d", MaxTranscriptLineRunes, len([]rune(out)))
	}
}

func TestTranscriptSearchBoundsWholeResult(t *testing.T) {
	// Ten max-length lines would blow 2000 chars; the result must stay bounded.
	hits := make([]TranscriptHit, 10)
	line := strings.Repeat("x", MaxTranscriptLineRunes)
	for i := range hits {
		hits[i] = TranscriptHit{Who: "W", Kind: "npc", Text: line}
	}
	out, err := NewTranscriptSearch(&fakeTranscript{hits: hits}).Execute(
		context.Background(), json.RawMessage(`{"query":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxToolResultChars {
		t.Errorf("result %d chars exceeds budget %d", len(out), MaxToolResultChars)
	}
}

func TestTranscriptSearchEmpty(t *testing.T) {
	out, err := NewTranscriptSearch(&fakeTranscript{}).Execute(
		context.Background(), json.RawMessage(`{"query":"nothing"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matching transcript lines" {
		t.Errorf("empty search = %q", out)
	}
}
