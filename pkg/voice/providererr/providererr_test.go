package providererr

import (
	"errors"
	"fmt"
	"testing"
)

// TestHTTPError_ErrorFormat pins the rendered message byte-for-byte: the doc
// comment promises it is identical to the adapters' pre-typed readErrorResponse
// output, and logs/string-compares downstream rely on that.
func TestHTTPError_ErrorFormat(t *testing.T) {
	err := &HTTPError{
		Op:         "elevenlabs.Transcribe",
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Body:       "rate limited",
	}
	want := "elevenlabs.Transcribe: HTTP 429 429 Too Many Requests: rate limited"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestHTTPError_AsThroughWrap pins the property the retry helper depends on
// (ADR-0044): errors.As must find the typed error through fmt.Errorf %w
// wrapping, with the status code intact — that is what makes classification
// structural instead of a substring match.
func TestHTTPError_AsThroughWrap(t *testing.T) {
	inner := &HTTPError{Op: "anthropic.Complete", StatusCode: 503, Status: "503 Service Unavailable", Body: "overloaded"}
	wrapped := fmt.Errorf("agenttool: llm round: %w", fmt.Errorf("start: %w", inner))

	var httpErr *HTTPError
	if !errors.As(wrapped, &httpErr) {
		t.Fatalf("errors.As failed to find *HTTPError through two wraps")
	}
	if httpErr.StatusCode != 503 {
		t.Fatalf("StatusCode through wrap = %d, want 503", httpErr.StatusCode)
	}
	if httpErr != inner {
		t.Fatalf("errors.As returned a different value than the wrapped one")
	}
}

// TestToolSyntaxError_MsgVerbatim pins the byte-preserved provider text: logs and
// downstream substring checks must see exactly what the vendor returned.
func TestToolSyntaxError_MsgVerbatim(t *testing.T) {
	msg := `400: {"error":{"code":"tool_use_failed","failed_generation":"<function=dice>"}}`
	err := &ToolSyntaxError{Op: "groq.Complete", Msg: msg}
	if got := err.Error(); got != msg {
		t.Fatalf("Error() = %q, want the verbatim provider message %q", got, msg)
	}

	var tse *ToolSyntaxError
	if !errors.As(fmt.Errorf("round 2: %w", err), &tse) {
		t.Fatalf("errors.As failed to find *ToolSyntaxError through a wrap")
	}
}

// TestToolSyntaxError_IsNotHTTPError pins the deliberate distinctness the doc
// comment promises: a tool-syntax failure must NOT classify as a transient
// HTTPError (it is a per-round policy signal for the agenttool bridge, never a
// back-off-and-retry case), even when wrapped.
func TestToolSyntaxError_IsNotHTTPError(t *testing.T) {
	wrapped := fmt.Errorf("start: %w", &ToolSyntaxError{Op: "groq.Complete", Msg: "tool_use_failed"})
	var httpErr *HTTPError
	if errors.As(wrapped, &httpErr) {
		t.Fatalf("*ToolSyntaxError matched *HTTPError; the types must stay distinct for retry classification")
	}
}
