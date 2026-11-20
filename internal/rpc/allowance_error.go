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
)

// allowanceExhaustedErr is the refusal for billing.ErrAllowanceExhausted.
func allowanceExhaustedErr() *connect.Error {
	err := connect.NewError(connect.CodeResourceExhausted,
		errors.New("this month's included usage is used up — AI features are paused until next month or a plan change"))
	err.Meta().Set(errorReasonHeader, reasonAllowanceExhausted)
	return err
}
