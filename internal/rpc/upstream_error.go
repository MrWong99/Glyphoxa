package rpc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// Upstream provider failures, told apart.
//
// Every GM-facing feature that spends a provider's quota used to answer ALL of
// its failures with one sentence, and every one of those sentences ended in
// "check the provider configuration". That is true for exactly one failure —
// a rejected key — and the one that actually happens in production is the other
// one: an exhausted quota returns HTTP 429 with a key that was ACCEPTED, so the
// GM is sent to re-check a correct setting while the real fix is billing, a
// plan change, or ten minutes.
//
// Both provider seams already carry the status as DATA, in two types because
// they are two seams: the image path returns [imagegen.StatusError], the text
// path returns [providererr.HTTPError] (ADR-0044). Reading both HERE, once, is
// what stops a surface from classifying the seam it happens to have been
// written against and silently collapsing the other — the failure mode that
// makes a branch dead in production while its unit test, which injects the
// other type through a fake, passes happily.

// upstreamRejection is a non-2xx from whichever provider a handler called,
// reduced to what a GM-facing message needs: the status, and who refused.
type upstreamRejection struct {
	// provider is the GM-facing noun, matching the vocabulary the Configuration
	// screen already uses: "image" or "LLM".
	provider string
	status   int
}

// classifyUpstream reports the upstream rejection err carries, if any. It reads
// through wrapping (errors.As), which is required: the enrichment job, llmcall
// and the assist engine all wrap on the way up.
func classifyUpstream(err error) (upstreamRejection, bool) {
	var ie *imagegen.StatusError
	if errors.As(err, &ie) {
		return upstreamRejection{provider: "image", status: ie.StatusCode}, true
	}
	var pe *providererr.HTTPError
	if errors.As(err, &pe) {
		return upstreamRejection{provider: "LLM", status: pe.StatusCode}, true
	}
	return upstreamRejection{}, false
}

// upstreamErr renders a classified upstream rejection as the error the GM sees,
// or nil when err is not one (the caller keeps its own default — an unclassified
// failure must not be given a fabricated cause).
//
// op is the log key; subject names what the GM asked for ("map generation",
// "content generation") so a mapper shared by several handlers cannot report one
// as another.
func upstreamErr(op, subject string, err error) *connect.Error {
	up, ok := classifyUpstream(err)
	if !ok {
		return nil
	}
	switch {
	case up.status == http.StatusTooManyRequests:
		// ResourceExhausted, the same code a spend cap returns (session.go): the
		// request was well-formed, the credential was ACCEPTED, and the account has
		// no allowance left. Warn rather than Error — an expected operating
		// condition of a metered key, not a defect.
		slog.Default().Warn(op+": provider quota or rate limit hit", "provider", up.provider, "err", err)
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
			"your %s provider is out of quota or rate-limited — check your plan, or try again shortly", up.provider))
	case up.status == http.StatusUnauthorized || up.status == http.StatusForbidden:
		// The one case the original sentence was written for. Both surfaces keep
		// their exact wording here, because here it is true.
		slog.Default().Error(op+": provider rejected the key", "provider", up.provider, "err", err)
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf(
			"%s failed — check the %s provider configuration and try again", subject, up.provider))
	case up.status >= 500:
		slog.Default().Error(op+": provider server error", "provider", up.provider, "status", up.status, "err", err)
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf(
			"the %s provider is having trouble right now — try again shortly", up.provider))
	}
	// A classified status nobody has a better answer for (a 400: a malformed
	// request, a safety refusal, or Gemini's billing-not-enabled precondition,
	// indistinguishable without parsing the body). The caller's default owns it —
	// guessing a cause is what this file exists to stop.
	return nil
}
