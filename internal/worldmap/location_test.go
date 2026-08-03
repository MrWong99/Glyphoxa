package worldmap_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/worldmap"
)

func set(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

// TestClause pins what the table may be told. The party's position is spoken
// aloud by whoever knows it, so a secret location in a prompt is a secret leaked
// at the table — exactly like a gm_private fact.
func TestClause(t *testing.T) {
	mapID, pinID := uuid.New(), uuid.New()

	cases := []struct {
		name   string
		marker storage.PartyMarker
		want   string
	}{
		{
			name:   "no marker set",
			marker: storage.PartyMarker{},
			want:   "",
		},
		{
			name: "at a pin on a map",
			marker: storage.PartyMarker{
				MapID: set(mapID), PinID: set(pinID),
				MapName: "Saltmarsh", PinLabel: "the Rusty Anchor",
			},
			want: "You are at the Rusty Anchor, in Saltmarsh.",
		},
		{
			name:   "on a map between pins",
			marker: storage.PartyMarker{MapID: set(mapID), MapName: "Saltmarsh"},
			want:   "You are in Saltmarsh.",
		},
		{
			// A private map is a place the table does not know exists.
			name: "gm_private map says nothing",
			marker: storage.PartyMarker{
				MapID: set(mapID), MapName: "The smugglers' routes", MapGMPrivate: true,
			},
			want: "",
		},
		{
			// Naming the surrounding area alone would be true and safe, but it would
			// also hint that something is here — so say nothing at all.
			name: "hidden pin says nothing, not even the area",
			marker: storage.PartyMarker{
				MapID: set(mapID), PinID: set(pinID),
				MapName: "Saltmarsh", PinLabel: "the smugglers' cache", PinHidden: true,
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := worldmap.Clause(tc.marker); got != tc.want {
				t.Errorf("Clause = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClauseIsOneShortClause pins the shape the ADR-0059 note sanctions: one
// sentence, well inside the block's bound. It is the justification for putting
// location in the prompt at all.
func TestClauseIsOneShortClause(t *testing.T) {
	got := worldmap.Clause(storage.PartyMarker{
		MapID: set(uuid.New()), PinID: set(uuid.New()),
		MapName: "Saltmarsh's harbour district", PinLabel: "the Rusty Anchor",
	})
	if got == "" {
		t.Fatal("expected a clause")
	}
	if n := len([]rune(got)); n > 160 {
		t.Errorf("clause is %d runes; it must stay one short clause", n)
	}
	if strings.Count(got, ".") != 1 {
		t.Errorf("clause = %q, want exactly one sentence", got)
	}
}
