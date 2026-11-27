package rpc

import (
	"errors"

	"connectrpc.com/connect"
)

// The plan-allowance refusal, said once.
//
// Every off-session paid call goes through one metering seam
// (billing.OffSession, #658), and every RPC in front of one — content
// drafting, map and portrait generation, pin suggestions, recaps, voice
// previews, Highlight sound, the planning chat — can therefore refuse for the
// same reason: the Tenant's monthly plan allowance is spent (ADR-0055 gate
// (b)). They all return THIS error, so the web renders one translated
// sentence instead of each surface inventing its own.
//
// The code is ResourceExhausted, which upstream_error.go and session.go also
// use for different conditions (a provider's own 429, the concurrent-session
// cap). The code alone therefore cannot tell the web WHICH sentence to show,
// and matching on the English message would break the moment it is reworded.
// So the refusal carries a machine-readable reason in error metadata, which
// Connect delivers as a response header (unary) or end-of-stream metadata
// (streaming); web/src/lib/connectError.ts reads it. The message stays a
// complete English sentence for any client that does not.

const (
	// errorReasonHeader names the error-metadata key carrying a stable,
	// machine-readable refusal reason.
	errorReasonHeader = "Glyphoxa-Error-Reason"
	// reasonAllowanceExhausted is the [errorReasonHeader] value for a spent
	// monthly plan allowance. The web matches on it: do not reword.
	reasonAllowanceExhausted = "allowance_exhausted"

	// The StartSession / StopSession refusals (session.go, #695). Their
	// messages are English sentences, so the Session screen translates on
	// these instead (web/src/screens/session/startStopError.ts). The web
	// matches on them: do not reword.
	reasonSessionActive         = "session_active"
	reasonDiscordNotConfigured  = "discord_not_configured"
	reasonBotTokenMissing       = "bot_token_missing"
	reasonBotTokenUndecryptable = "bot_token_undecryptable"
	reasonVoiceUnavailable      = "voice_unavailable"
	reasonSessionLimit          = "session_limit"
	reasonShuttingDown          = "shutting_down"
	reasonWorkerPending         = "worker_pending"
	reasonStartCancelled        = "start_cancelled"
	reasonNoActiveSession       = "no_active_session"
	reasonStopPending           = "stop_pending"
)

// reasonErr builds a refusal that carries reason in [errorReasonHeader] next
// to its code and English message.
func reasonErr(code connect.Code, reason, msg string) *connect.Error {
	err := connect.NewError(code, errors.New(msg))
	err.Meta().Set(errorReasonHeader, reason)
	return err
}

// allowanceExhaustedErr is the refusal for billing.ErrAllowanceExhausted.
func allowanceExhaustedErr() *connect.Error {
	return reasonErr(connect.CodeResourceExhausted, reasonAllowanceExhausted,
		"this month's included usage is used up — AI features are paused until next month or a plan change")
}
