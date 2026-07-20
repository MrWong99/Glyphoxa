package session

import (
	"errors"
	"strings"
)

// Start-precondition fail codes (#483 M4). In a split deployment a worker's
// Manager.Start refusal crosses the claim plane as intent.last_error — a plain
// string — so the typed sentinels (ErrDiscordNotConfigured, allowance exhausted,
// …) used to flatten into IntentControl's generic fmt.Errorf and the RPC's
// default CodeInternal "internal error", losing the actionable guidance the
// -mode all path gives. EncodeStartFailure stamps a stable machine-parseable
// prefix ("code=<code>: <message>") onto the recorded last_error, and
// DecodeStartFailure re-maps it to the same sentinel so both deployment shapes
// produce identical connect codes and messages. The codes are part of the
// claim-plane record: rename only with a migration story.
const failCodePrefix = "code="

// startFailCodes pairs each typed Start refusal with its durable code. Ordered
// list (not a map) so encode/decode are deterministic.
var startFailCodes = []struct {
	code string
	err  error
}{
	{"session_active", ErrSessionActive},
	{"session_limit", ErrSessionLimit},
	{"discord_not_configured", ErrDiscordNotConfigured},
	{"discord_token_missing", ErrDiscordTokenMissing},
	{"discord_token_undecryptable", ErrDiscordTokenUndecryptable},
	{"voice_unavailable", ErrVoiceUnavailable},
	{"allowance_exhausted", ErrAllowanceExhausted},
	{"manager_closed", ErrManagerClosed},
}

// EncodeStartFailure renders a Manager.Start refusal for intent.last_error:
// "code=<code>: <message>" when err matches a typed precondition sentinel, else
// the plain message (no code — DecodeStartFailure then reports no match and the
// caller keeps its generic path).
func EncodeStartFailure(err error) string {
	for _, fc := range startFailCodes {
		if errors.Is(err, fc.err) {
			return failCodePrefix + fc.code + ": " + err.Error()
		}
	}
	return err.Error()
}

// DecodeStartFailure maps an encoded last_error back to its typed sentinel. ok
// is false for a last_error without a (known) code — older rows, plain loop
// errors, or a future code this binary does not know.
func DecodeStartFailure(lastError string) (error, bool) {
	rest, found := strings.CutPrefix(lastError, failCodePrefix)
	if !found {
		return nil, false
	}
	code, _, _ := strings.Cut(rest, ":")
	for _, fc := range startFailCodes {
		if fc.code == code {
			return fc.err, true
		}
	}
	return nil, false
}
