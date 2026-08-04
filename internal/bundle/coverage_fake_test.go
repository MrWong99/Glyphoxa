package bundle_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
)

// Bundle coverage for the world epic #533 added (#547).
//
// A silent omission in a backup is worse than a missing feature, because it is
// discovered only when someone restores. TestExportImportFake_RoundTrip compares
// a bundle to its re-export, which catches a lossy IMPORT — but not a lossy
// EXPORT, since both sides would drop the same thing and compare equal. So these
// tests assert against the bundle's own contents.

func exportSeeded(t *testing.T, opts bundle.ExportOptions) (*bundle.Bundle, *fakeStore, uuid.UUID) {
	t.Helper()
	src := newFakeStore()
	campaignID, _ := seedFakeCampaign(t, src)
	b, err := bundle.Export(context.Background(), src, campaignID, opts)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return b, src, campaignID
}

// TestExport_CarriesEverythingTheEpicAdded is the anti-vacuity guard for the
// round-trip test: it names each section explicitly, so an exporter that quietly
// stopped writing one fails HERE rather than comparing equal to itself.
func TestExport_CarriesEverythingTheEpicAdded(t *testing.T) {
	t.Parallel()
	b, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeHistory: true})

	var npc *bundle.Node
	for i := range b.Campaign.Nodes {
		if len(b.Campaign.Nodes[i].Aspects) > 0 {
			npc = &b.Campaign.Nodes[i]
		}
	}
	if npc == nil {
		t.Fatal("no node carried Aspects — the export dropped them")
	}
	// Private aspects MUST be exported: an export that dropped them would restore
	// a campaign missing exactly its secrets.
	var sawPrivate bool
	for _, a := range npc.Aspects {
		if a.GMPrivate {
			sawPrivate = true
		}
	}
	if !sawPrivate {
		t.Error("the GM-private aspect was dropped from the backup")
	}
	if len(npc.Tags) == 0 {
		t.Error("tags were dropped")
	}

	if len(b.Campaign.Edges) == 0 || b.Campaign.Edges[0].Note == "" ||
		b.Campaign.Edges[0].Disposition == 0 {
		t.Errorf("the relation's texture was dropped: %+v", b.Campaign.Edges)
	}
	if len(b.Campaign.Maps) != 2 {
		t.Fatalf("maps = %d, want 2", len(b.Campaign.Maps))
	}
	var pins int
	for _, m := range b.Campaign.Maps {
		pins += len(m.Pins)
	}
	if pins == 0 {
		t.Error("pins were dropped")
	}
	if len(b.Campaign.Boards) == 0 || len(b.Campaign.Boards[0].NodeIDs) == 0 {
		t.Errorf("boards were dropped: %+v", b.Campaign.Boards)
	}
	if b.FormatVersion != bundle.FormatVersion {
		t.Errorf("format_version = %d", b.FormatVersion)
	}
}

// TestExport_ImagesAreOptIn: the blob cap is 32 MiB PER image and base64 inflates
// by a third, so a setup export must stay something a person can mail.
func TestExport_ImagesAreOptIn(t *testing.T) {
	t.Parallel()
	without, _, _ := exportSeeded(t, bundle.ExportOptions{})
	for _, m := range without.Campaign.Maps {
		if m.ImageBase64 != "" {
			t.Fatalf("map %q carried its image without the flag", m.Name)
		}
	}
	// And the map itself still round-trips: name, nesting, anchor, privacy, pins.
	// Losing the picture is not losing the map.
	if len(without.Campaign.Maps) != 2 {
		t.Fatalf("maps = %d without images, want both", len(without.Campaign.Maps))
	}

	with, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeImages: true})
	var carried int
	for _, m := range with.Campaign.Maps {
		if m.ImageBase64 != "" {
			carried++
			if m.ContentType == "" {
				t.Errorf("map %q carried bytes with no content type", m.Name)
			}
		}
	}
	if carried != 1 {
		t.Errorf("maps carrying an image = %d, want the one that has bytes", carried)
	}
}

// TestExport_AppearancesFollowTheHistoryFlag: an appearance is a record of what
// was SAID, not part of the campaign's setup — and it is derived, so a
// destination that re-indexes gets them back anyway.
func TestExport_AppearancesFollowTheHistoryFlag(t *testing.T) {
	t.Parallel()
	setup, _, _ := exportSeeded(t, bundle.ExportOptions{})
	if setup.Campaign.History != nil {
		t.Fatal("a setup export carried history")
	}

	full, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeHistory: true})
	var appearances int
	for _, s := range full.Campaign.History.Sessions {
		appearances += len(s.Appearances)
	}
	if appearances == 0 {
		t.Error("appearances were dropped from a history export")
	}
}

// TestImport_RemapsEveryNewCrossReference is the AC. Every new table references
// something by id, and an import that got one wrong would produce a campaign that
// looks whole and points at another campaign's rows.
func TestImport_RemapsEveryNewCrossReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeHistory: true, IncludeImages: true})

	dst := newFakeStore()
	res, err := bundle.Import(ctx, dst, uuid.New(), b)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Maps != 2 || res.Pins == 0 || res.Boards == 0 || res.Aspects == 0 || res.Tags == 0 {
		t.Fatalf("import counts look wrong: %+v", res)
	}

	// The nesting survived. This is the one a single-pass import breaks: the child
	// map sorts alphabetically BEFORE its parent, so importing in bundle order
	// would reference a parent that does not exist yet.
	maps, err := dst.ListMaps(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}
	var nested, parentID uuid.UUID
	for _, m := range maps {
		if m.Name == "Saltmarsh" {
			parentID = m.ID
		}
		if m.ParentMapID.Valid {
			nested = m.ID
		}
	}
	if nested == uuid.Nil {
		t.Fatal("the nested map lost its parent")
	}
	for _, m := range maps {
		if m.ID == nested && m.ParentMapID.UUID != parentID {
			t.Errorf("the nested map points at %s, not the imported parent %s", m.ParentMapID.UUID, parentID)
		}
		// Every id must be a DESTINATION id: a remap that leaked a source id would
		// produce a campaign quietly pointing into the campaign it was copied from.
		if m.AnchorNodeID.Valid && !dst.nodeInCampaign(m.AnchorNodeID.UUID, res.CampaignID) {
			t.Errorf("map %q anchors a node outside the imported campaign", m.Name)
		}
	}

	// Pins resolve to imported nodes, on imported maps.
	for _, m := range maps {
		pins, err := dst.ListPins(ctx, res.CampaignID, m.ID)
		if err != nil {
			t.Fatalf("ListPins: %v", err)
		}
		for _, p := range pins {
			if !dst.nodeInCampaign(p.NodeID, res.CampaignID) {
				t.Errorf("pin on %q references a node outside the imported campaign", m.Name)
			}
		}
	}

	// Boards resolve, in order.
	boards, err := dst.ListBoards(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) == 0 || len(boards[0].NodeIDs) != 2 {
		t.Fatalf("board entries did not survive: %+v", boards)
	}
	for _, id := range boards[0].NodeIDs {
		if !dst.nodeInCampaign(id, res.CampaignID) {
			t.Error("a board entry points outside the imported campaign")
		}
	}

	// The image bytes landed through the blob seam, under a NEW key — the source
	// key names the source tenant, and reusing it would let one tenant's import
	// overwrite another tenant's picture.
	if len(dst.images) != 1 {
		t.Fatalf("imported images = %d, want 1", len(dst.images))
	}
	for key := range dst.images {
		if _, reused := src(t).images[key]; reused {
			t.Errorf("the import reused the SOURCE blob key %q", key)
		}
	}
}

// src builds a throwaway seeded source purely to compare blob keys against.
func src(t *testing.T) *fakeStore {
	t.Helper()
	f := newFakeStore()
	seedFakeCampaign(t, f)
	return f
}

// TestImport_AcceptsAV1Bundle: bumping the format must not orphan yesterday's
// backups. Every v2 addition is omitempty, so a v1 bundle is a valid v2 bundle
// with those sections absent.
func TestImport_AcceptsAV1Bundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _, _ := exportSeeded(t, bundle.ExportOptions{})
	// Strip everything v2 added and label it v1, exactly as an old export would.
	b.FormatVersion = 1
	b.Campaign.Maps = nil
	b.Campaign.Boards = nil
	for i := range b.Campaign.Nodes {
		b.Campaign.Nodes[i].Aspects = nil
		b.Campaign.Nodes[i].Tags = nil
	}
	for i := range b.Campaign.Edges {
		b.Campaign.Edges[i].Note = ""
		b.Campaign.Edges[i].Disposition = 0
	}

	res, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
	if err != nil {
		t.Fatalf("a v1 bundle must still import: %v", err)
	}
	if res.Nodes == 0 {
		t.Error("the v1 bundle imported nothing")
	}
	if res.Maps != 0 || res.Aspects != 0 {
		t.Errorf("a v1 bundle produced v2 rows: %+v", res)
	}
}

// TestImport_UnknownRefsAreFatal: a bundle is all-or-nothing. A map anchored to a
// node that is not in the bundle would import as a map pointing at nothing.
func TestImport_UnknownRefsAreFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, mutate := range map[string]func(*bundle.Bundle){
		"map anchor": func(b *bundle.Bundle) { b.Campaign.Maps[0].AnchorNodeID = "nope" },
		"map parent": func(b *bundle.Bundle) { b.Campaign.Maps[0].ParentMapID = "nope" },
		"pin node": func(b *bundle.Bundle) {
			for i := range b.Campaign.Maps {
				if len(b.Campaign.Maps[i].Pins) > 0 {
					b.Campaign.Maps[i].Pins[0].NodeID = "nope"
				}
			}
		},
		"board entry": func(b *bundle.Bundle) { b.Campaign.Boards[0].NodeIDs[0] = "nope" },
	} {
		t.Run(name, func(t *testing.T) {
			b, _, _ := exportSeeded(t, bundle.ExportOptions{})
			mutate(b)
			if _, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b); err == nil {
				t.Fatalf("an unknown %s ref must fail the whole import", name)
			}
		})
	}
}

// TestImport_FailureDropsTheImagesItWrote. Blob writes are OUTSIDE the import
// transaction — blob.NewPostgres runs on its own pool — so the rollback that
// removes the rows leaves the bytes. Without an explicit sweep they would be
// orphaned with no row naming them and no job that would ever find them.
func TestImport_FailureDropsTheImagesItWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeImages: true})
	// Break something AFTER the maps are written: a board pointing nowhere.
	b.Campaign.Boards[0].NodeIDs[0] = "nope"

	dst := newFakeStore()
	if _, err := bundle.Import(ctx, dst, uuid.New(), b); err == nil {
		t.Fatal("the import should have failed")
	}
	if len(dst.images) != 0 {
		t.Fatalf("a failed import stranded %d image(s)", len(dst.images))
	}
	if len(dst.deletedImages) == 0 {
		t.Error("nothing was swept — the bytes were never written, or never cleaned")
	}
}

// TestImport_RejectsAnImageWithNoBlobStore rather than silently importing a map
// whose picture is gone.
func TestImport_RejectsAnImageWithNoBlobStore(t *testing.T) {
	t.Parallel()
	b, _, _ := exportSeeded(t, bundle.ExportOptions{IncludeImages: true})
	if _, err := bundle.Import(context.Background(), noBlobTx{inner: newFakeStore()}, uuid.New(), b); err == nil {
		t.Fatal("importing image bytes with no blob store should fail loudly")
	}
}

// noBlobTx is a TxRunner that deliberately does NOT implement MapImageWriter.
// The inner store is a NAMED FIELD, not embedded: embedding would promote
// WriteMapImage and the type assertion inside Import would succeed, making this
// test pass for exactly the wrong reason.
type noBlobTx struct{ inner *fakeStore }

func (n noBlobTx) InTx(ctx context.Context, fn func(tx bundle.ImportStore) error) error {
	return n.inner.InTx(ctx, fn)
}
