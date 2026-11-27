package providererr

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
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

// errorResponse is a hand-built non-2xx response with the given body reader.
func errorResponse(code int, status string, body io.Reader) *http.Response {
	return &http.Response{StatusCode: code, Status: status, Body: io.NopCloser(body)}
}

// TestFromResponse_ErrorFormat pins that the shared constructor renders the same
// literal [TestHTTPError_ErrorFormat] does — the adapters' message must not move
// now that they all build it here — with the body snippet trimmed.
func TestFromResponse_ErrorFormat(t *testing.T) {
	resp := errorResponse(429, "429 Too Many Requests", strings.NewReader("  rate limited\n"))
	err := FromResponse(resp, "elevenlabs.Transcribe")

	want := "elevenlabs.Transcribe: HTTP 429 429 Too Many Requests: rate limited"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	var httpErr *HTTPError
	if !errors.As(fmt.Errorf("start: %w", err), &httpErr) {
		t.Fatalf("errors.As failed to find *HTTPError through a wrap")
	}
	if httpErr.Op != "elevenlabs.Transcribe" || httpErr.StatusCode != 429 || httpErr.Status != "429 Too Many Requests" {
		t.Fatalf("HTTPError = %+v, want Op/StatusCode/Status carried from the response", httpErr)
	}
}

// TestFromResponse_BodySnippet pins the snippet contract [HTTPError] documents:
// at most 512 bytes of the body, and an empty body is an empty snippet.
func TestFromResponse_BodySnippet(t *testing.T) {
	long := strings.Repeat("x", 2000)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"truncated to the cap", long, long[:errorSnippetBytes]},
		{"exactly the cap", long[:errorSnippetBytes], long[:errorSnippetBytes]},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FromResponse(errorResponse(500, "500 Internal Server Error", strings.NewReader(tt.body)), "anthropic.Complete")
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("FromResponse returned %T, want *HTTPError", err)
			}
			if httpErr.Body != tt.want {
				t.Fatalf("Body is %d bytes, want %d", len(httpErr.Body), len(tt.want))
			}
		})
	}
}

// TestFromResponse_BodyReadError pins that a body failing mid-read still yields
// the typed error with the status intact and whatever was read before the
// failure: retry classification needs the status code, not the snippet.
func TestFromResponse_BodyReadError(t *testing.T) {
	body := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("connection reset")))
	err := FromResponse(errorResponse(503, "503 Service Unavailable", body), "elevenlabs.Synthesize")

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("FromResponse returned %T, want *HTTPError", err)
	}
	if httpErr.StatusCode != 503 || httpErr.Status != "503 Service Unavailable" {
		t.Fatalf("status = %d %q, want 503 %q", httpErr.StatusCode, httpErr.Status, "503 Service Unavailable")
	}
	if httpErr.Body != "partial" {
		t.Fatalf("Body = %q, want the bytes read before the failure %q", httpErr.Body, "partial")
	}
}

// TestFromResponse_LeavesBodyOpen pins that closing stays with the caller: the
// adapters defer resp.Body.Close() themselves.
func TestFromResponse_LeavesBodyOpen(t *testing.T) {
	body := &closeSpy{Reader: strings.NewReader("nope")}
	_ = FromResponse(&http.Response{StatusCode: 400, Status: "400 Bad Request", Body: body}, "elevenlabs.ListVoices")
	if body.closed {
		t.Fatalf("FromResponse closed resp.Body; the caller owns the close")
	}
}

type closeSpy struct {
	io.Reader
	closed bool
}

func (c *closeSpy) Close() error { c.closed = true; return nil }

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
