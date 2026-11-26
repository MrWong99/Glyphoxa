package auth

import (
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
)

// This file is THE GUEST GATE (ADR-0065): the one place that decides what a
// Campaign Guest may reach at all. It lives in the policy layer so the Connect
// interceptor and the guarded plain mounts share it. The rule is default-deny:
// a request whose session resolves to a guest workspace is refused with
// CodePermissionDenied / 403 BEFORE the handler runs unless its procedure (or
// mount) declares a [GuestPosture] the guest's rights satisfy. The zero
// posture admits no guest, so a new RPC or mount is GM-only until someone
// decides otherwise — and TestGuestGate_EveryProcedureIsClassified fails until
// they have decided one way or the other.
//
// The gate answers "may this guest call this at all". What a reachable handler
// then returns is still its own job: every guest-reachable handler pins the
// Active Campaign to the guest Campaign, scopes by-id reads to it, and
// withholds what the guest's rights do not cover.

// GuestPosture is what a procedure or mount requires of a Campaign Guest. The
// zero value is GM-only on purpose: forgetting to declare one denies.
type GuestPosture struct {
	// reachable is false for the zero (GM-only) posture.
	reachable bool
	// anyOf lists the rights of which the guest must hold at least one. Empty
	// with reachable set means every guest, whatever their rights.
	anyOf []GuestRight
}

// GuestAny admits every Campaign Guest, whatever rights they hold — for the
// handful of procedures a guest shell cannot work without.
func GuestAny() GuestPosture { return GuestPosture{reachable: true} }

// GuestWith admits a Campaign Guest holding at least one of the given rights.
// With no rights it is the GM-only zero posture, never [GuestAny]: an empty
// requirement must not read as "no requirement".
func GuestWith(anyOf ...GuestRight) GuestPosture {
	if len(anyOf) == 0 {
		return GuestPosture{}
	}
	return GuestPosture{reachable: true, anyOf: append([]GuestRight(nil), anyOf...)}
}

// Reachable reports whether ANY guest can pass this posture.
func (g GuestPosture) Reachable() bool { return g.reachable }

// Rights returns the rights of which one is required (nil for [GuestAny] and
// for the GM-only posture; tell those apart with [GuestPosture.Reachable]).
func (g GuestPosture) Rights() []GuestRight { return append([]GuestRight(nil), g.anyOf...) }

// Admits reports whether a guest holding rights passes.
func (g GuestPosture) Admits(rights RightSet) bool {
	if !g.reachable {
		return false
	}
	if len(g.anyOf) == 0 {
		return true
	}
	for _, r := range g.anyOf {
		if rights.Has(r) {
			return true
		}
	}
	return false
}

// guestProcedures maps every guest-reachable Connect procedure to what it
// requires. Anything absent is GM-only. Each entry is a deliberate decision;
// the ones that need a reason carry it.
var guestProcedures = map[string]GuestPosture{
	// ── every guest ─────────────────────────────────────────────────────────
	// The shell: who am I, what can I see, sign out, switch workspace.
	managementv1connect.AuthServiceGetCurrentUserProcedure:   GuestAny(),
	managementv1connect.AuthServiceLogoutProcedure:           GuestAny(),
	managementv1connect.AuthServiceGetAdmissionModeProcedure: GuestAny(),
	managementv1connect.AuthServiceGetInvitationProcedure:    GuestAny(),
	managementv1connect.AuthServiceSwitchWorkspaceProcedure:  GuestAny(),
	// The header: which Campaign this is. Pinned to the guest Campaign.
	managementv1connect.CampaignServiceGetActiveCampaignProcedure: GuestAny(),
	// A static registry read (language codes) the header renders with.
	managementv1connect.CampaignServiceListSupportedLanguagesProcedure: GuestAny(),

	// AdminService is deployment-scoped and ignores the workspace: a Platform
	// Admin who happens to be acting as someone's guest must still be able to
	// administer the instance. Every one of its handlers requires a Platform
	// Admin itself (rpc.AdminServer.requireAdmin), so for anyone else these
	// are refused there, exactly as they are for a GM.
	managementv1connect.AdminServiceGetAccessOverviewProcedure:      GuestAny(),
	managementv1connect.AdminServiceGrantAccessProcedure:            GuestAny(),
	managementv1connect.AdminServiceRevokeAccessProcedure:           GuestAny(),
	managementv1connect.AdminServiceCreateAccessInvitationProcedure: GuestAny(),
	managementv1connect.AdminServiceRevokeAccessInvitationProcedure: GuestAny(),
	managementv1connect.AdminServiceSetUserSuspendedProcedure:       GuestAny(),
	managementv1connect.AdminServiceSetTenantPlatformKeysProcedure:  GuestAny(),

	// ── wiki.read ───────────────────────────────────────────────────────────
	managementv1connect.CampaignServiceListNodesProcedure:         GuestWith(RightWikiRead),
	managementv1connect.CampaignServiceListNodeEdgesProcedure:     GuestWith(RightWikiRead),
	managementv1connect.CampaignServiceGetKnowledgeGraphProcedure: GuestWith(RightWikiRead),
	managementv1connect.CampaignServiceGetCampaignTagsProcedure:   GuestWith(RightWikiRead),
	managementv1connect.CampaignServiceListBoardsProcedure:        GuestWith(RightWikiRead),
	managementv1connect.CampaignServiceSearchNodesProcedure:       GuestWith(RightWikiRead),
	// Appearances quote transcript text; the handler returns none without
	// transcripts.read as well.
	managementv1connect.CampaignServiceListNodeAppearancesProcedure: GuestWith(RightWikiRead),

	// ── wiki.edit ───────────────────────────────────────────────────────────
	// NOT here: everything that spends the host's LLM/image budget
	// (GenerateKnowledge, GeneratePersona, portraits, proposals) and
	// SetNodeAgent, which rewires who voices an NPC.
	managementv1connect.CampaignServiceCreateNodeProcedure:        GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceUpdateNodeProcedure:        GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceDeleteNodeProcedure:        GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceCreateEdgeProcedure:        GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceDeleteEdgeProcedure:        GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceUpdateEdgeDetailsProcedure: GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceSetNodeTagsProcedure:       GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceRenameTagProcedure:         GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceCreateBoardProcedure:       GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceUpdateBoardProcedure:       GuestWith(RightWikiEdit),
	managementv1connect.CampaignServiceDeleteBoardProcedure:       GuestWith(RightWikiEdit),

	// ── maps.read / maps.edit ───────────────────────────────────────────────
	// NOT here: CreateMap, UpdateMap, DeleteMap, ReplaceMapImage (the map
	// itself is the GM's) and GenerateMapImage / SuggestMapPins (they spend).
	// The Party Marker has no RPC — it moves through the Butler's tools and
	// slash commands only — so maps.edit is the three Pin writes.
	managementv1connect.CampaignServiceListMapsProcedure:   GuestWith(RightMapsRead),
	managementv1connect.CampaignServiceGetMapViewProcedure: GuestWith(RightMapsRead),
	managementv1connect.CampaignServiceCreatePinProcedure:  GuestWith(RightMapsEdit),
	managementv1connect.CampaignServiceUpdatePinProcedure:  GuestWith(RightMapsEdit),
	managementv1connect.CampaignServiceDeletePinProcedure:  GuestWith(RightMapsEdit),

	// ── transcripts.read ────────────────────────────────────────────────────
	managementv1connect.SessionServiceSearchTranscriptLinesProcedure: GuestWith(RightTranscriptsRead),
	managementv1connect.SessionServiceSearchTranscriptsProcedure:     GuestWith(RightTranscriptsRead),
	// ListSessions carries session metadata only (when, how long, how many
	// lines) and no transcript text. A guest with highlights.read needs it
	// too: ListHighlights is per session, so without the list they could not
	// find the Highlights they were granted.
	managementv1connect.SessionServiceListSessionsProcedure: GuestWith(RightTranscriptsRead, RightHighlightsRead),

	// ── highlights.read ─────────────────────────────────────────────────────
	// Promoted Highlights only; the handlers never serve a candidate to a guest.
	managementv1connect.SessionServiceListHighlightsProcedure:   GuestWith(RightHighlightsRead),
	managementv1connect.SessionServiceGetHighlightProcedure:     GuestWith(RightHighlightsRead),
	managementv1connect.SessionServiceSearchHighlightsProcedure: GuestWith(RightHighlightsRead),

	// ── planning.use ────────────────────────────────────────────────────────
	// All of ChatService, in threads the guest created. An exchange spends the
	// HOST Tenant's LLM budget — that is what granting the right means.
	managementv1connect.ChatServiceListPlanningThreadsProcedure:  GuestWith(RightPlanningUse),
	managementv1connect.ChatServiceGetPlanningThreadProcedure:    GuestWith(RightPlanningUse),
	managementv1connect.ChatServiceCreatePlanningThreadProcedure: GuestWith(RightPlanningUse),
	managementv1connect.ChatServiceRenamePlanningThreadProcedure: GuestWith(RightPlanningUse),
	managementv1connect.ChatServiceDeletePlanningThreadProcedure: GuestWith(RightPlanningUse),
	managementv1connect.ChatServicePlanningExchangeProcedure:     GuestWith(RightPlanningUse),
}

// gmOnlyProcedures names every procedure a Campaign Guest can NEVER call. The
// gate does not consult it — absence from [guestProcedures] already denies —
// it exists so that being GM-only is something someone wrote down:
// TestGuestGate_EveryProcedureIsClassified requires every procedure of every
// management service to appear in exactly one of the two tables.
var gmOnlyProcedures = map[string]struct{}{
	// Tenant and Campaign lifecycle.
	managementv1connect.AuthServiceRenameTenantProcedure:                {},
	managementv1connect.CampaignServiceListCampaignsProcedure:           {},
	managementv1connect.CampaignServiceCreateCampaignProcedure:          {},
	managementv1connect.CampaignServiceUpdateCampaignProcedure:          {},
	managementv1connect.CampaignServiceSetActiveCampaignProcedure:       {},
	managementv1connect.CampaignServiceArchiveCampaignProcedure:         {},
	managementv1connect.CampaignServiceUnarchiveCampaignProcedure:       {},
	managementv1connect.CampaignServiceDeleteCampaignProcedure:          {},
	managementv1connect.CampaignServiceListDiscordVoiceMembersProcedure: {},

	// Managing guests is the GM's alone, whatever rights a guest holds.
	managementv1connect.CampaignServiceListCampaignAccessProcedure:       {},
	managementv1connect.CampaignServiceAddCampaignGuestProcedure:         {},
	managementv1connect.CampaignServiceUpdateCampaignGuestProcedure:      {},
	managementv1connect.CampaignServiceRemoveCampaignGuestProcedure:      {},
	managementv1connect.CampaignServiceCreateCampaignInvitationProcedure: {},
	managementv1connect.CampaignServiceRevokeCampaignInvitationProcedure: {},

	// Roster, cast, Agents, Tool Grants, Player Characters.
	managementv1connect.CampaignServiceGetCampaignRosterProcedure:   {},
	managementv1connect.CampaignServiceCreateAgentProcedure:         {},
	managementv1connect.CampaignServiceUpdateAgentProcedure:         {},
	managementv1connect.CampaignServiceDeleteAgentProcedure:         {},
	managementv1connect.CampaignServiceSetNodeAgentProcedure:        {},
	managementv1connect.CampaignServiceGetRosterReadinessProcedure:  {},
	managementv1connect.CampaignServiceGetAgentFactPreviewProcedure: {},
	managementv1connect.CampaignServiceListToolGrantsProcedure:      {},
	managementv1connect.CampaignServiceUpdateToolGrantProcedure:     {},
	managementv1connect.CampaignServiceListCharactersProcedure:      {},
	managementv1connect.CampaignServiceCreateCharacterProcedure:     {},
	managementv1connect.CampaignServiceUpdateCharacterProcedure:     {},
	managementv1connect.CampaignServiceDeleteCharacterProcedure:     {},

	// Knowledge work that spends the host's budget, and the GM's review queue.
	managementv1connect.CampaignServiceFindDuplicateEntriesProcedure:     {},
	managementv1connect.CampaignServiceGeneratePersonaProcedure:          {},
	managementv1connect.CampaignServiceGenerateKnowledgeProcedure:        {},
	managementv1connect.CampaignServiceApplyGeneratedKnowledgeProcedure:  {},
	managementv1connect.CampaignServiceListKnowledgeProposalsProcedure:   {},
	managementv1connect.CampaignServiceApproveKnowledgeProposalProcedure: {},
	managementv1connect.CampaignServiceRejectKnowledgeProposalProcedure:  {},
	managementv1connect.CampaignServiceListSimilarKnowledgeProcedure:     {},
	managementv1connect.CampaignServiceGenerateNodePortraitProcedure:     {},
	managementv1connect.CampaignServiceSetNodePortraitProcedure:          {},
	managementv1connect.CampaignServiceClearNodePortraitProcedure:        {},

	// The maps themselves.
	managementv1connect.CampaignServiceCreateMapProcedure:        {},
	managementv1connect.CampaignServiceUpdateMapProcedure:        {},
	managementv1connect.CampaignServiceGenerateMapImageProcedure: {},
	managementv1connect.CampaignServiceSuggestMapPinsProcedure:   {},
	managementv1connect.CampaignServiceReplaceMapImageProcedure:  {},
	managementv1connect.CampaignServiceDeleteMapProcedure:        {},

	// Provider keys, Discord, spend, usage.
	managementv1connect.ProviderServiceListProviderConfigsProcedure:  {},
	managementv1connect.ProviderServiceSaveProviderConfigProcedure:   {},
	managementv1connect.ProviderServiceDeleteProviderConfigProcedure: {},
	managementv1connect.ProviderServiceGetUsageSummaryProcedure:      {},
	managementv1connect.ProviderServiceSaveDiscordSettingsProcedure:  {},
	managementv1connect.ProviderServiceReleaseDiscordGuildProcedure:  {},
	managementv1connect.ProviderServiceResolveGuildInviteProcedure:   {},
	managementv1connect.ProviderServiceGetSpendCapsProcedure:         {},
	managementv1connect.ProviderServiceSetSpendCapsProcedure:         {},
	managementv1connect.VoiceServiceListVoicesProcedure:              {},
	managementv1connect.VoiceServicePreviewVoiceProcedure:            {},
	managementv1connect.VoiceServiceListModelsProcedure:              {},
	managementv1connect.VoiceServiceGetProviderHealthProcedure:       {},

	// Running a Voice Session: the GM console.
	managementv1connect.SessionServiceGetSessionProcedure:               {},
	managementv1connect.SessionServiceStartSessionProcedure:             {},
	managementv1connect.SessionServiceListSessionVoiceChannelsProcedure: {},
	managementv1connect.SessionServiceStopSessionProcedure:              {},
	managementv1connect.SessionServiceSetAgentMuteProcedure:             {},
	managementv1connect.SessionServiceSetAllMuteProcedure:               {},
	managementv1connect.SessionServiceGenerateRecapProcedure:            {},

	// Curating and sharing Highlights.
	managementv1connect.SessionServicePromoteHighlightProcedure:  {},
	managementv1connect.SessionServiceDeleteHighlightProcedure:   {},
	managementv1connect.SessionServiceSetHighlightSoundProcedure: {},
	managementv1connect.SessionServiceListShareChannelsProcedure: {},
	managementv1connect.SessionServiceShareHighlightProcedure:    {},
}

// GuestProcedurePosture returns what a Connect procedure requires of a
// Campaign Guest: the GM-only zero posture for anything not in the table.
func GuestProcedurePosture(procedure string) GuestPosture {
	return guestProcedures[procedure]
}

// GuestProcedures returns a copy of the guest-reachable procedure table, for
// tests and tooling that need to enumerate it.
func GuestProcedures() map[string]GuestPosture {
	out := make(map[string]GuestPosture, len(guestProcedures))
	for proc, posture := range guestProcedures {
		out[proc] = posture
	}
	return out
}

// GMOnlyProcedure reports whether the procedure is explicitly classified
// GM-only.
func GMOnlyProcedure(procedure string) bool {
	_, ok := gmOnlyProcedures[procedure]
	return ok
}
