package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/MrWong99/Glyphoxa/internal/discordrest"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// Provider-health failure classes. GetProviderHealth used to put err.Error()
// on the wire, so the Setup screen showed a GM "failed_precondition: llmbuild:
// tenant has no platform-key entitlement (BYOK plan) — … (ADR-0054)" or
// "groq: missing API key (set GROQ_API_KEY)". Each degraded result now carries
// one of these stable codes (ProviderHealth.code, which the web translates)
// plus a short English sentence as the fallback detail; the raw error is logged.
const (
	healthMissingKey  = "missing_key"
	healthKeyRejected = "key_rejected"
	healthQuota       = "quota_exhausted"
	healthUnreachable = "unreachable"
	healthNoPlatform  = "plan_excludes_platform_keys"
	healthCheckFailed = "check_failed"
)

// errMissingKey marks a health probe with no key to try: none saved and none in
// the deployment env (or, for Discord, no saved and no central Bot token).
var errMissingKey = errors.New("no API key configured")

// healthClass maps a provider check failure onto its stable code and the GM
// sentence shown when the client does not translate the code.
func healthClass(err error) (code, detail string) {
	switch {
	case errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
		return healthNoPlatform, "Your plan doesn't include our keys — paste your own key."
	case errors.Is(err, errMissingKey):
		return healthMissingKey, "No key yet — paste one to use this service."
	}
	if status, ok := httpStatusOf(err); ok {
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return healthKeyRejected, "The key was rejected — check it or paste a new one."
		case status == http.StatusTooManyRequests:
			return healthQuota, "Out of quota or rate-limited — check your plan, or try again later."
		case status >= 500:
			return healthUnreachable, "The service is having trouble — try again shortly."
		}
		return healthCheckFailed, "The check failed — try again shortly."
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) {
		return healthUnreachable, "Couldn't reach the service — try again shortly."
	}
	return healthCheckFailed, "The check failed — try again shortly."
}

// httpStatusOf reads a provider's non-2xx status out of err, through wrapping,
// from whichever seam produced it: the text/TTS adapters and our own pings
// ([providererr.HTTPError]), the image path ([imagegen.StatusError]) and the
// Discord REST calls ([discordrest.StatusError]).
func httpStatusOf(err error) (int, bool) {
	var pe *providererr.HTTPError
	if errors.As(err, &pe) {
		return pe.StatusCode, true
	}
	var ie *imagegen.StatusError
	if errors.As(err, &ie) {
		return ie.StatusCode, true
	}
	var de *discordrest.StatusError
	if errors.As(err, &de) {
		return de.Status, true
	}
	return 0, false
}
