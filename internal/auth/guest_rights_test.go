package auth_test

import (
	"slices"
	"testing"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// TestNormalizeGuestRights pins the ADR-0065 vocabulary rules: implications
// are applied, unknowns and duplicates dropped, and the order is canonical
// whatever order the input came in.
func TestNormalizeGuestRights(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []auth.GuestRight
		want []auth.GuestRight
	}{
		{"nil is the empty set, never nil", nil, []auth.GuestRight{}},
		{"a plain right stays alone", []auth.GuestRight{auth.RightTranscriptsRead}, []auth.GuestRight{auth.RightTranscriptsRead}},
		{"wiki.read does not imply secrets", []auth.GuestRight{auth.RightWikiRead}, []auth.GuestRight{auth.RightWikiRead}},
		{"wiki.secrets does not imply read", []auth.GuestRight{auth.RightWikiSecrets}, []auth.GuestRight{auth.RightWikiSecrets}},
		{"wiki.edit implies read and secrets", []auth.GuestRight{auth.RightWikiEdit},
			[]auth.GuestRight{auth.RightWikiRead, auth.RightWikiSecrets, auth.RightWikiEdit}},
		{"maps.edit implies maps.read", []auth.GuestRight{auth.RightMapsEdit},
			[]auth.GuestRight{auth.RightMapsRead, auth.RightMapsEdit}},
		{"maps.read does not imply anything", []auth.GuestRight{auth.RightMapsRead}, []auth.GuestRight{auth.RightMapsRead}},
		{"planning.use implies wiki read and secrets, not transcripts", []auth.GuestRight{auth.RightPlanningUse},
			[]auth.GuestRight{auth.RightWikiRead, auth.RightWikiSecrets, auth.RightPlanningUse}},
		{"highlights.read does not imply transcripts", []auth.GuestRight{auth.RightHighlightsRead}, []auth.GuestRight{auth.RightHighlightsRead}},
		{"unknown values are dropped", []auth.GuestRight{"wiki.admin", "", "WIKI.READ", auth.RightMapsRead},
			[]auth.GuestRight{auth.RightMapsRead}},
		{"duplicates collapse and order is canonical",
			[]auth.GuestRight{auth.RightPlanningUse, auth.RightMapsRead, auth.RightWikiRead, auth.RightMapsRead, auth.RightPlanningUse},
			[]auth.GuestRight{auth.RightWikiRead, auth.RightWikiSecrets, auth.RightMapsRead, auth.RightPlanningUse}},
		{"everything", []auth.GuestRight{auth.RightPlanningUse, auth.RightHighlightsRead, auth.RightTranscriptsRead, auth.RightMapsEdit, auth.RightWikiEdit},
			[]auth.GuestRight{auth.RightWikiRead, auth.RightWikiSecrets, auth.RightWikiEdit, auth.RightMapsRead, auth.RightMapsEdit,
				auth.RightTranscriptsRead, auth.RightHighlightsRead, auth.RightPlanningUse}},
	}
	for _, tc := range cases {
		got := auth.NormalizeGuestRights(tc.in)
		if got == nil {
			t.Errorf("%s: got nil, want a non-nil slice (it is written to a NOT NULL text[] column)", tc.name)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: NormalizeGuestRights(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
		// Normalizing is idempotent: what is stored reads back unchanged.
		if again := auth.NormalizeGuestRights(got); !slices.Equal(again, got) {
			t.Errorf("%s: not idempotent: %v -> %v", tc.name, got, again)
		}
	}
}

// TestGuestRightVocabulary pins the stored strings. They are persisted in
// campaign_guest.rights and invitation.rights, so renaming one silently
// revokes it from every existing guest.
func TestGuestRightVocabulary(t *testing.T) {
	t.Parallel()
	want := []auth.GuestRight{
		"wiki.read", "wiki.secrets", "wiki.edit", "maps.read", "maps.edit",
		"transcripts.read", "highlights.read", "planning.use",
	}
	if got := auth.GuestRights(); !slices.Equal(got, want) {
		t.Fatalf("GuestRights() = %v, want %v", got, want)
	}
}

func TestRightSet(t *testing.T) {
	t.Parallel()
	var zero auth.RightSet
	for _, r := range auth.GuestRights() {
		if zero.Has(r) {
			t.Errorf("the zero RightSet has %s", r)
		}
	}
	if len(zero.List()) != 0 {
		t.Errorf("zero RightSet lists %v", zero.List())
	}

	s := auth.NewRightSet(auth.RightPlanningUse)
	for _, r := range []auth.GuestRight{auth.RightPlanningUse, auth.RightWikiRead, auth.RightWikiSecrets} {
		if !s.Has(r) {
			t.Errorf("planning.use set lacks %s", r)
		}
	}
	for _, r := range []auth.GuestRight{auth.RightWikiEdit, auth.RightTranscriptsRead, auth.RightMapsRead, auth.RightHighlightsRead, "nonsense"} {
		if s.Has(r) {
			t.Errorf("planning.use set has %s", r)
		}
	}

	// A stored row is normalized on read, so a row written before an
	// implication existed still behaves as the vocabulary says.
	stored := auth.RightSetFromStrings([]string{"maps.edit", "bogus"})
	if !stored.Has(auth.RightMapsRead) || !stored.Has(auth.RightMapsEdit) || len(stored.List()) != 2 {
		t.Errorf("RightSetFromStrings([maps.edit bogus]) = %v, want maps.read + maps.edit", stored.List())
	}
}

// TestGuestRightProtoMapping: every proto GuestRight except UNSPECIFIED maps
// to a vocabulary string and back, and every vocabulary string has a proto
// value. A right added to the contract but not to the mapping would be
// silently dropped on the wire — a GM would tick a box that grants nothing.
func TestGuestRightProtoMapping(t *testing.T) {
	t.Parallel()
	seen := map[auth.GuestRight]bool{}
	for num, name := range managementv1.GuestRight_name {
		pb := managementv1.GuestRight(num)
		got := auth.GuestRightsFromProto([]managementv1.GuestRight{pb})
		if pb == managementv1.GuestRight_GUEST_RIGHT_UNSPECIFIED {
			if len(got) != 0 {
				t.Errorf("UNSPECIFIED maps to %v, want nothing", got)
			}
			continue
		}
		// FromProto normalizes, so pick the one right that maps straight back.
		var direct []auth.GuestRight
		for _, r := range got {
			if back := auth.GuestRightsToProto([]auth.GuestRight{r}); len(back) == 1 && back[0] == pb {
				direct = append(direct, r)
			}
		}
		if len(direct) != 1 {
			t.Errorf("proto %s does not round-trip: FromProto = %v", name, got)
			continue
		}
		seen[direct[0]] = true
	}
	for _, r := range auth.GuestRights() {
		if !seen[r] {
			t.Errorf("vocabulary right %q has no proto GuestRight value", r)
		}
		if pb := auth.GuestRightsToProto([]auth.GuestRight{r}); len(pb) != 1 {
			t.Errorf("GuestRightsToProto(%q) = %v, want exactly one value", r, pb)
		}
	}

	// The wire path normalizes too: unknown numbers are dropped, implications
	// applied.
	got := auth.GuestRightsFromProto([]managementv1.GuestRight{
		managementv1.GuestRight(9999), managementv1.GuestRight_GUEST_RIGHT_UNSPECIFIED,
		managementv1.GuestRight_GUEST_RIGHT_WIKI_EDIT,
	})
	want := []auth.GuestRight{auth.RightWikiRead, auth.RightWikiSecrets, auth.RightWikiEdit}
	if !slices.Equal(got, want) {
		t.Errorf("GuestRightsFromProto = %v, want %v", got, want)
	}
}
