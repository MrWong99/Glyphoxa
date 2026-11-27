package storage

import (
	"slices"
	"strconv"
)

// AccessPolicy is the one thing about admission the database cannot know by
// itself (ADR-0065): whether this deployment requires a Tenant's operator to
// be ADMITTED — a Platform Admin, or the holder of an Access Grant — for that
// Tenant to count as operated.
//
// On an `allowlist` deployment operating a Tenant is granted by a Platform
// Admin and revoked by deleting the grant. The web session of a revoked
// operator is cut per request ([Store.ResolveWorkspace]); this policy carries
// the same rule to everything that hangs off the operator rather than off
// their session:
//
//   - GM identity on Discord ([Store.ListTenantOperatorBindings],
//     [Store.ListTenantOperatorDiscordIDs]): a revoked operator is no longer a
//     GM of their Tenant;
//   - the Tenant's Campaign Guests and campaign invitation links (the host
//     standing rule, see hostStandingClause): a Campaign of a Tenant whose
//     operator is not admitted is not reachable for its guests, and its links
//     do not redeem.
//
// The zero value requires nothing, which is `open` Admission Mode (there,
// self-signup admits and suspension revokes) and GLYPHOXA_DEV_MODE. The
// composition root sets it once with [Store.SetAccessPolicy], from the same
// effective Admission Mode and Platform Admin list the boot sweep
// ([Store.RevokeSessionsOutsideAllowlist]) is given.
//
// Suspension is independent of all this: a suspended operator has no standing
// in any Admission Mode.
type AccessPolicy struct {
	// OperatorsNeedGrant is true on an `allowlist` deployment.
	OperatorsNeedGrant bool
	// PlatformAdmins are the Discord snowflakes admitted without a grant
	// (GLYPHOXA_OPERATOR_IDS), as this process was configured with them.
	PlatformAdmins []string
	// RecordedAdmins additionally trusts the Platform Admin list the web tier
	// recorded at its last boot ([Store.RecordPlatformAdmins]). It is for a
	// process that is NOT given GLYPHOXA_OPERATOR_IDS — a voice worker — and
	// so cannot name the Platform Admins itself. While no list has ever been
	// recorded such a process requires nothing: it has no way to tell a
	// Platform Admin from a revoked operator, and dropping the deployment's
	// own admin from GM standing is the worse failure. The web tier knows the
	// list first-hand and leaves this false.
	RecordedAdmins bool
}

// SetAccessPolicy installs the deployment's operator-standing rule. Call it
// once at boot, before the Store is handed to anything that runs concurrently:
// the field is read without synchronization.
func (s *Store) SetAccessPolicy(p AccessPolicy) {
	p.PlatformAdmins = slices.Clone(p.PlatformAdmins)
	if p.PlatformAdmins == nil {
		p.PlatformAdmins = []string{}
	}
	s.access = p
}

// accessArgs returns the three query arguments every standing clause below
// takes, in order: whether a grant is required, the Platform Admin snowflakes
// (never nil — a NULL array would make `= ANY` unknown rather than false),
// and whether the recorded list counts too.
func (s *Store) accessArgs() (needGrant bool, admins []string, recorded bool) {
	admins = s.access.PlatformAdmins
	if admins == nil {
		admins = []string{}
	}
	return s.access.OperatorsNeedGrant, admins, s.access.RecordedAdmins
}

// operatorAdmittedClause is the SQL test "the users row aliased userAlias is
// admitted to operate a Tenant under the deployment's [AccessPolicy]": no
// grant is required, or they are a Platform Admin, or they hold an Access
// Grant. The three [Store.accessArgs] values sit at positions arg, arg+1 and
// arg+2 of the query. Suspension is NOT part of it — callers add that.
func operatorAdmittedClause(userAlias string, arg int) string {
	need, admins, recorded := "$"+strconv.Itoa(arg), "$"+strconv.Itoa(arg+1), "$"+strconv.Itoa(arg+2)
	id := userAlias + ".discord_user_id"
	return `(NOT ` + need + `::boolean
		OR ` + id + ` = ANY(` + admins + `::text[])
		OR (` + recorded + `::boolean AND (
		      NOT EXISTS (SELECT 1 FROM deployment_settings ds WHERE ds.id AND ds.platform_admin_ids IS NOT NULL)
		      OR EXISTS (SELECT 1 FROM deployment_settings ds WHERE ds.id AND ` + id + ` = ANY(ds.platform_admin_ids))))
		OR EXISTS (SELECT 1 FROM access_grant ag WHERE ag.discord_user_id = ` + id + `))`
}

// hostStandingClause is the HOST STANDING rule every guest-facing read and
// the campaign-link redemption share (ADR-0065 review hardening): a Campaign
// is reachable for its guests only while the operator of its Tenant — the
// tenant row aliased tenantAlias — is in good standing, i.e. not suspended
// and admitted under the deployment's [AccessPolicy]. Without it, revoking or
// suspending a GM would leave every guest of theirs (a second account of the
// same person included) working in the Tenant that was just cut off.
//
// A Tenant with NO operator is treated as before this rule existed: its
// Campaigns stay reachable. The guest rows are never touched, so re-granting
// or un-suspending the operator restores every guest exactly as they were.
// arg is as in [operatorAdmittedClause].
func hostStandingClause(tenantAlias string, arg int) string {
	return `(` + tenantAlias + `.operator_user_id IS NULL OR EXISTS (
		SELECT 1 FROM users ho
		 WHERE ho.id = ` + tenantAlias + `.operator_user_id
		   AND ho.suspended_at IS NULL
		   AND ` + operatorAdmittedClause("ho", arg) + `))`
}
