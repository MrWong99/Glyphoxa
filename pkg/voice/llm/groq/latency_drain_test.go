// Unit tests + implementation for the live TTFT probe's per-call drain and
// classification logic (issue #155). Deliberately NOT behind the `live` build
// tag: the drain loop is pure channel-consumption over [llm.StreamEvent], so
// its failure classification (provider EventError vs. truncation vs. success)
// is pinned here against fake event streams in the default keyless suite
// (ADR-0033). The live-tagged probe (latency_live_test.go) reuses drainStream
// for the real Groq calls.
package groq_test

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// errTruncated marks a stream whose channel closed with neither an
// [llm.EventDone] nor an [llm.EventError] — per the [llm.Provider] contract
// that stream was cancelled and its text must not count as a complete reply.
var errTruncated = errors.New("stream truncated: closed without EventDone or EventError")

// errProvider marks a mid-stream [llm.EventError]; the vendor's detail is
// wrapped around it so errors.Is classification and the log line both work.
var errProvider = errors.New("provider stream error")

// failureKind buckets a failed call for the summary: "provider-error" (the
// vendor emitted a mid-stream EventError), "truncated" (channel closed with
// no terminal event, e.g. ctx timeout), or "call-failed" (Complete never
// returned a stream — bad key, non-2xx, ...).
func failureKind(err error) string {
	switch {
	case errors.Is(err, errProvider):
		return "provider-error"
	case errors.Is(err, errTruncated):
		return "truncated"
	default:
		return "call-failed"
	}
}

// failSummary formats a per-tier failure count for the summary, kinds sorted
// for a stable log line: "n=3 (provider-error=2 truncated=1)".
func failSummary(byKind map[string]int) string {
	var n int
	kinds := make([]string, 0, len(byKind))
	for k, c := range byKind {
		n += c
		kinds = append(kinds, k)
	}
	if n == 0 {
		return "n=0"
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%s=%d", k, byKind[k])
	}
	return fmt.Sprintf("n=%d (%s)", n, strings.Join(parts, " "))
}

// drainStream consumes one completion stream and classifies the outcome. It
// returns the time from start to the first non-empty content token, the total
// wall time, and the accumulated text. A sample is successful (err == nil)
// only if the stream delivered a terminal completion; a mid-stream
// [llm.EventError] fails it with the provider's detail.
func drainStream(start time.Time, ch <-chan llm.StreamEvent) (first, total time.Duration, text string, err error) {
	var sawFirst, done, failed bool
	var provErr string
	for ev := range ch {
		switch ev.Type {
		case llm.EventText:
			if ev.Text == "" {
				continue
			}
			if !sawFirst {
				first = time.Since(start)
				sawFirst = true
			}
			text += ev.Text
		case llm.EventDone:
			done = true
		case llm.EventError:
			failed = true
			provErr = ev.Err
		}
	}
	total = time.Since(start)
	if !sawFirst {
		first = total
	}
	if failed {
		return first, total, text, fmt.Errorf("%w: %s", errProvider, provErr)
	}
	if !done {
		return first, total, text, errTruncated
	}
	return first, total, text, nil
}

// feed returns a closed channel pre-loaded with the given events — a fake
// provider stream per the [llm.Provider] contract.
func feed(events ...llm.StreamEvent) <-chan llm.StreamEvent {
	ch := make(chan llm.StreamEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch
}

// A mid-stream provider failure (llm.EventError) must fail the sample with
// the provider's error detail — before #155 the drain loop swallowed it and
// recorded the call as a successful latency sample.
func TestDrainStream_MidStreamProviderError_FailsSample(t *testing.T) {
	ch := feed(
		llm.StreamEvent{Type: llm.EventText, Text: "Ein Bier"},
		llm.StreamEvent{Type: llm.EventError, Err: "rate limited by vendor"},
	)
	_, _, _, err := drainStream(time.Now(), ch)
	if err == nil {
		t.Fatal("mid-stream EventError must fail the sample, got err == nil")
	}
	if !strings.Contains(err.Error(), "rate limited by vendor") {
		t.Fatalf("error must carry the provider's detail, got: %v", err)
	}
}

// A channel that closes with neither EventDone nor EventError was cancelled
// (e.g. the probe's 60s ctx timeout): the accumulated text is truncated, so
// the sample must fail as a truncation — before #155 it was recorded as a
// successful sample with first == total when no token had arrived.
func TestDrainStream_CloseWithoutTerminalEvent_FailsAsTruncation(t *testing.T) {
	ch := feed(
		llm.StreamEvent{Type: llm.EventText, Text: "Ein halbes"},
	)
	_, _, _, err := drainStream(time.Now(), ch)
	if err == nil {
		t.Fatal("close without terminal event must fail the sample, got err == nil")
	}
	if !errors.Is(err, errTruncated) {
		t.Fatalf("want errTruncated, got: %v", err)
	}
}

// A stream that ends in EventDone is a successful sample and measures exactly
// what the probe measured before #155: first-token time stamped at the first
// NON-EMPTY EventText, total at channel close, text accumulated across chunks.
func TestDrainStream_TerminalDone_SucceedsWithLatencySample(t *testing.T) {
	ch := feed(
		llm.StreamEvent{Type: llm.EventText, Text: ""}, // empty delta: not a content token
		llm.StreamEvent{Type: llm.EventText, Text: "Ein "},
		llm.StreamEvent{Type: llm.EventText, Text: "Bier."},
		llm.StreamEvent{Type: llm.EventDone, StopReason: "stop"},
	)
	first, total, text, err := drainStream(time.Now(), ch)
	if err != nil {
		t.Fatalf("terminal EventDone must succeed, got: %v", err)
	}
	if text != "Ein Bier." {
		t.Fatalf("accumulated text: want %q, got %q", "Ein Bier.", text)
	}
	if first <= 0 || total < first {
		t.Fatalf("want 0 < first <= total, got first=%v total=%v", first, total)
	}
}

// failureKind buckets each failed call for the summary so the probe reports
// WHAT failed (provider error vs. truncation vs. call never started), not
// just how many — that attribution is the probe's purpose per #28.
func TestFailureKind_BucketsByCause(t *testing.T) {
	_, _, _, provErr := drainStream(time.Now(), feed(
		llm.StreamEvent{Type: llm.EventError, Err: "boom"},
	))
	_, _, _, truncErr := drainStream(time.Now(), feed())
	for _, tc := range []struct {
		err  error
		want string
	}{
		{provErr, "provider-error"},
		{truncErr, "truncated"},
		{errors.New("401 unauthorized"), "call-failed"},
	} {
		if got := failureKind(tc.err); got != tc.want {
			t.Errorf("failureKind(%v): want %q, got %q", tc.err, tc.want, got)
		}
	}
}

// The summary reports failures separately from the latency distribution:
// total count plus a deterministic per-kind breakdown.
func TestFailSummary_CountsByKind(t *testing.T) {
	if got := failSummary(nil); got != "n=0" {
		t.Errorf("no failures: want %q, got %q", "n=0", got)
	}
	got := failSummary(map[string]int{"truncated": 1, "provider-error": 2})
	want := "n=3 (provider-error=2 truncated=1)"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
