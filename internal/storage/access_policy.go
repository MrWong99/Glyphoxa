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
// composition root sets it once with [Store.SetAccessPolicy]: on the web tier
// from the same effective Admission Mode and Platform Admin list the boot
// sweep ([Store.RevokeSessionsOutsideAllowlist]) is given; on a process that
// is configured with neither, see RecordedAdmins.
//
// Suspension is independent of all this: a suspended operator has no standing
// in any Admission Mode.
type AccessPolicy struct {
	// OperatorsNeedGrant is true on an `allowlist` deployment.
	OperatorsNeedGrant bool
	// PlatformAdmins are the Discord snowflakes admitted without a grant
	// (GLYPHOXA_OPERATOR_IDS), as this process was configured with them.
	PlatformAdmins []string
	// RecordedAdmins is for a process that is NOT configured with the
	// deployment's access posture — a voice worker, which is given neither
	// GLYPHOXA_OPERATOR_IDS nor GLYPHOXA_ADMISSION_MODE and is not restarted
	// when the web tier's values change. Such a process reads both LIVE, on
	// every query, from what the web tier recorded at its last boot
	// (deployment_settings: [Store.RecordAdmissionPosture],
	// [Store.RecordPlatformAdmins]):
	//
	//   - the recorded Platform Admin list counts in addition to
	//     PlatformAdmins;
	//   - a recorded Admission Mode of `open` requires nothing, whatever
	//     OperatorsNeedGrant says. A worker therefore sets OperatorsNeedGrant
	//     unconditionally and lets the row decide: it follows a flip in either
	//     direction within one read instead of latching the mode it booted
	//     under;
	//   - while no admin list has ever been recorded (no row, or a row from
	//     before the column existed) it requires nothing: the process cannot
	//     tell a Platform Admin from a revoked operator, and dropping the
	//     deployment's own admin — or every GM — from GM standing is the
	//     worse failure.
	//
	// The web tier knows both first-hand and leaves this false.
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
// Grant. For a RecordedAdmins process the recorded posture is read here, live
// (see the field). The three [Store.accessArgs] values sit at positions arg, arg+1 and
// arg+2 of the query. Suspension is NOT part of it — callers add that.
func operatorAdmittedClause(userAlias string, arg int) string {
	need, admins, recorded := "$"+strconv.Itoa(arg), "$"+strconv.Itoa(arg+1), "$"+strconv.Itoa(arg+2)
	id := userAlias + ".discord_user_id"
	return `(NOT ` + need + `::boolean
		OR ` + id + ` = ANY(` + admins + `::text[])
		OR (` + recorded + `::boolean AND (
		      NOT EXISTS (SELECT 1 FROM deployment_settings ds WHERE ds.id AND ds.platform_admin_ids IS NOT NULL)
		      OR EXISTS (SELECT 1 FROM deployment_settings ds WHERE ds.id AND ds.admission_mode = 'open')
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
