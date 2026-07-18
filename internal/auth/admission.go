package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Admission Mode (ADR-0055): how a deployment admits web identities.
//   - allowlist: exactly ADR-0041 — the operator allowlist is the sole gate.
//   - open:      any Discord User completing OAuth is admitted; a stranger's
//     signup founds a fresh Tenant (create-only). The allowlist survives as
//     the platform-administration list, not as the admission gate.
//
// Distinct from Mode, the process role (-mode voice|web|all). GLYPHOXA_DEV_MODE
// preempts Admission Mode entirely (the dev boot never consults it).

// AdmissionMode is the validated admission posture vocabulary.
type AdmissionMode string

const (
	// AdmissionAllowlist is the default: ADR-0041 behavior, unchanged.
	AdmissionAllowlist AdmissionMode = "allowlist"
	// AdmissionOpen admits any Discord User completing OAuth (ADR-0055).
	AdmissionOpen AdmissionMode = "open"
)

// ParseAdmissionMode validates an admission-mode string (trimmed, lowercased).
// Unlike the log-format enum precedent, an unknown value is a loud error, not a
// silent default — a typo'd posture must never silently run allowlist-locked or,
// worse, open. The empty string is also an error: callers decide what "unset"
// means (the boot falls back to the persisted posture, then to allowlist).
func ParseAdmissionMode(s string) (AdmissionMode, error) {
	switch AdmissionMode(strings.ToLower(strings.TrimSpace(s))) {
	case AdmissionAllowlist:
		return AdmissionAllowlist, nil
	case AdmissionOpen:
		return AdmissionOpen, nil
	}
	return "", fmt.Errorf("auth: invalid admission mode %q (want %q or %q)",
		s, AdmissionAllowlist, AdmissionOpen)
}

// SignupProvisioner is the extra persistence open-mode admission needs: the
// ADR-0055 all-or-nothing signup transaction. *storage.Store satisfies it.
type SignupProvisioner interface {
	ProvisionSignup(ctx context.Context, p storage.SignupParams) (storage.SignupResult, error)
}

// Admission is the OAuth callback's admission policy (ADR-0055). The zero
// value fails closed to allowlist posture. In AdmissionOpen, Signup and
// SignupPlanSlug must be set (the boot preflight guarantees the slug resolves
// to a live plan); a nil Signup falls back to allowlist posture with a loud
// log rather than admitting strangers it cannot provision.
type Admission struct {
	Mode           AdmissionMode
	Allowlist      OperatorAllowlist
	SignupPlanSlug string
	Signup         SignupProvisioner
}

// open reports whether this policy actually admits strangers: open mode with a
// working provisioner.
func (a Admission) open() bool {
	return a.Mode == AdmissionOpen && a.Signup != nil
}
