// Package providererr carries the typed HTTP error the provider adapters surface
// on a non-2xx start response, so the retry helper (pkg/voice/retry) can classify
// a call by status code via [errors.As] instead of string-matching the message
// (ADR-0044). The [HTTPError.Error] text is byte-identical to the adapters'
// previous readErrorResponse output, so nothing downstream that logs or compares
// the message changes.
package providererr

import "fmt"

// HTTPError is a non-2xx response from a provider's start call — the STT POST,
// the TTS POST, or the LLM Messages request. It is what makes retry
// classification structural: a 429 or 5xx is retryable, other 4xx are not, and
// the check is a type assertion on this, never a substring match.
//
// Op is the adapter operation name INCLUDING its package prefix (e.g.
// "elevenlabs.Transcribe", "anthropic.Complete") so [HTTPError.Error] reproduces
// the exact string the adapter used before it was typed. Body is the trimmed
// response snippet (up to 512 bytes) the adapters already read for diagnostics.
type HTTPError struct {
	Op         string
	StatusCode int
	Status     string
	Body       string
}

// Error renders "<Op>: HTTP <StatusCode> <Status>: <Body>", byte-identical to the
// adapters' pre-typed-error readErrorResponse message.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s: %s", e.Op, e.StatusCode, e.Status, e.Body)
}
