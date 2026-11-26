package auth

import (
	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
)

// GuestRight is one capability a GM grants a Campaign Guest on one Campaign
// (ADR-0065). The string form is what campaign_guest.rights and
// invitation.rights store; the proto enum mirrors it ([guestRightProto] is the
// one mapping). Rights are additive, and an unknown string grants nothing:
// nothing in the code base tests for a right outside this vocabulary.
type GuestRight string

const (
	// RightWikiRead reads the world wiki — public material only.
	RightWikiRead GuestRight = "wiki.read"
	// RightWikiSecrets additionally reads gm_private Nodes, private Aspects and
	// everything else a GM marked private (Maps, Pins).
	RightWikiSecrets GuestRight = "wiki.secrets"
	// RightWikiEdit creates, edits and deletes wiki entries, Edges, tags and
	// boards.
	RightWikiEdit GuestRight = "wiki.edit"
	// RightMapsRead views Maps and their Pins.
	RightMapsRead GuestRight = "maps.read"
	// RightMapsEdit adds, moves and deletes Pins.
	RightMapsEdit GuestRight = "maps.edit"
	// RightTranscriptsRead reads and searches past sessions' transcripts.
	RightTranscriptsRead GuestRight = "transcripts.read"
	// RightHighlightsRead sees promoted Session Highlights. Never candidates.
	RightHighlightsRead GuestRight = "highlights.read"
	// RightPlanningUse uses the Butler planning chat in threads of their own.
	RightPlanningUse GuestRight = "planning.use"
)

// guestRightVocabulary is every Guest Right, in the canonical order
// [NormalizeGuestRights] emits.
var guestRightVocabulary = []GuestRight{
	RightWikiRead, RightWikiSecrets, RightWikiEdit,
	RightMapsRead, RightMapsEdit,
	RightTranscriptsRead, RightHighlightsRead, RightPlanningUse,
}

// guestRightImplies is the ADR-0065 implication table. A right that cannot
// honestly be narrower than what it reveals carries the rights it reveals:
// an editor blind to private Aspects would erase them on save, and the Butler
// answers from the whole Knowledge Graph.
var guestRightImplies = map[GuestRight][]GuestRight{
	RightWikiEdit:    {RightWikiRead, RightWikiSecrets},
	RightMapsEdit:    {RightMapsRead},
	RightPlanningUse: {RightWikiRead, RightWikiSecrets},
}

// GuestRights lists the whole vocabulary in canonical order (a copy).
func GuestRights() []GuestRight {
	return append([]GuestRight(nil), guestRightVocabulary...)
}

// NormalizeGuestRights returns the set a grant actually stores: unknown values
// and duplicates dropped, every implied right added, in canonical order. The
// result is never nil, so it writes to a NOT NULL text[] column as '{}'.
func NormalizeGuestRights(in []GuestRight) []GuestRight {
	have := make(map[GuestRight]bool, len(guestRightVocabulary))
	for _, r := range in {
		have[r] = true
		for _, implied := range guestRightImplies[r] {
			have[implied] = true
		}
	}
	out := make([]GuestRight, 0, len(guestRightVocabulary))
	for _, r := range guestRightVocabulary {
		if have[r] {
			out = append(out, r)
		}
	}
	return out
}

// GuestRightsFromStrings reads a stored rights column: normalized, so a row
// written before an implication existed (or by hand) still behaves as the
// vocabulary says.
func GuestRightsFromStrings(in []string) []GuestRight {
	rights := make([]GuestRight, len(in))
	for i, s := range in {
		rights[i] = GuestRight(s)
	}
	return NormalizeGuestRights(rights)
}

// GuestRightStrings is the storage form of a rights list.
func GuestRightStrings(in []GuestRight) []string {
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = string(r)
	}
	return out
}

// RightSet is a Campaign Guest's normalized rights, as the request carries
// them. The zero value holds nothing.
type RightSet struct {
	has map[GuestRight]struct{}
}

// NewRightSet builds the set, applying the implications.
func NewRightSet(rights ...GuestRight) RightSet {
	norm := NormalizeGuestRights(rights)
	s := RightSet{has: make(map[GuestRight]struct{}, len(norm))}
	for _, r := range norm {
		s.has[r] = struct{}{}
	}
	return s
}

// RightSetFromStrings builds the set from a stored rights column.
func RightSetFromStrings(in []string) RightSet {
	return NewRightSet(GuestRightsFromStrings(in)...)
}

// Has reports whether the set holds the right.
func (s RightSet) Has(r GuestRight) bool {
	_, ok := s.has[r]
	return ok
}

// List returns the rights in canonical order.
func (s RightSet) List() []GuestRight {
	out := make([]GuestRight, 0, len(s.has))
	for _, r := range guestRightVocabulary {
		if s.Has(r) {
			out = append(out, r)
		}
	}
	return out
}

// guestRightProto is the ONE mapping between the proto enum and the stored
// string. TestGuestRightProtoMapping fails when a proto value other than
// UNSPECIFIED is missing here, so a right added to the contract cannot be
// silently dropped on the wire.
var guestRightProto = map[managementv1.GuestRight]GuestRight{
	managementv1.GuestRight_GUEST_RIGHT_WIKI_READ:        RightWikiRead,
	managementv1.GuestRight_GUEST_RIGHT_WIKI_SECRETS:     RightWikiSecrets,
	managementv1.GuestRight_GUEST_RIGHT_WIKI_EDIT:        RightWikiEdit,
	managementv1.GuestRight_GUEST_RIGHT_MAPS_READ:        RightMapsRead,
	managementv1.GuestRight_GUEST_RIGHT_MAPS_EDIT:        RightMapsEdit,
	managementv1.GuestRight_GUEST_RIGHT_TRANSCRIPTS_READ: RightTranscriptsRead,
	managementv1.GuestRight_GUEST_RIGHT_HIGHLIGHTS_READ:  RightHighlightsRead,
	managementv1.GuestRight_GUEST_RIGHT_PLANNING_USE:     RightPlanningUse,
}

// guestRightToProto is [guestRightProto] inverted, built once.
var guestRightToProto = func() map[GuestRight]managementv1.GuestRight {
	m := make(map[GuestRight]managementv1.GuestRight, len(guestRightProto))
	for pb, r := range guestRightProto {
		m[r] = pb
	}
	return m
}()

// GuestRightsFromProto maps wire rights onto the normalized vocabulary.
// UNSPECIFIED and values this server does not know are dropped: a client
// cannot grant a right the server cannot enforce.
func GuestRightsFromProto(in []managementv1.GuestRight) []GuestRight {
	rights := make([]GuestRight, 0, len(in))
	for _, pb := range in {
		if r, ok := guestRightProto[pb]; ok {
			rights = append(rights, r)
		}
	}
	return NormalizeGuestRights(rights)
}

// GuestRightsToProto maps rights onto the wire, in the order given; a right
// with no proto value (there is none today) is dropped.
func GuestRightsToProto(in []GuestRight) []managementv1.GuestRight {
	out := make([]managementv1.GuestRight, 0, len(in))
	for _, r := range in {
		if pb, ok := guestRightToProto[r]; ok {
			out = append(out, pb)
		}
	}
	return out
}
