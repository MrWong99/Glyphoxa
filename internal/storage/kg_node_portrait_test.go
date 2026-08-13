//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Node portrait persistence (#590): the row half of the portrait feature. The
// blob-seam ordering lives in the RPC suite; what is covered HERE is what only
// a real Postgres shows — the old-key CTE handoff, campaign scoping, the
// updated_at bump, and the sweep listing.

func TestSetNodePortrait_Lifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	before, _, err := st.NodePortrait(ctx, campaignID, bart.ID)
	if err != nil || before != "" {
		t.Fatalf("a fresh node: key = %q, err = %v; want empty, nil", before, err)
	}

	// First write: no previous key to release.
	set, old, err := st.SetNodePortrait(ctx, campaignID, bart.ID, "t/x/node/a/portrait")
	if err != nil {
		t.Fatalf("SetNodePortrait: %v", err)
	}
	if old != "" {
		t.Errorf("first write returned old key %q, want none", old)
	}
	if set.PortraitBlobKey != "t/x/node/a/portrait" {
		t.Errorf("row key = %q", set.PortraitBlobKey)
	}
	if !set.UpdatedAt.After(bart.UpdatedAt) {
		t.Error("updated_at did not advance — the portrait URL's cache validator is stale")
	}

	// Replace: the PREVIOUS key comes back so the caller can release its bytes.
	_, old, err = st.SetNodePortrait(ctx, campaignID, bart.ID, "t/x/node/b/portrait")
	if err != nil {
		t.Fatalf("SetNodePortrait replace: %v", err)
	}
	if old != "t/x/node/a/portrait" {
		t.Errorf("replace returned old key %q, want the first key", old)
	}

	// The serve read agrees with the write.
	key, updatedAt, err := st.NodePortrait(ctx, campaignID, bart.ID)
	if err != nil || key != "t/x/node/b/portrait" || updatedAt.IsZero() {
		t.Fatalf("NodePortrait = (%q, %v, %v)", key, updatedAt, err)
	}

	// Clear: '' lands, and the cleared key comes back for release.
	cleared, old, err := st.SetNodePortrait(ctx, campaignID, bart.ID, "")
	if err != nil {
		t.Fatalf("SetNodePortrait clear: %v", err)
	}
	if old != "t/x/node/b/portrait" || cleared.PortraitBlobKey != "" {
		t.Errorf("clear = (old %q, key %q)", old, cleared.PortraitBlobKey)
	}
}

// TestSetNodePortrait_IsCampaignScoped: a Node in another Campaign is invisible
// to the write (#342) — cross-campaign mutation is refused server-side.
func TestSetNodePortrait_IsCampaignScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	otherCampaign := insertCampaign(t, st, tenantID, "Second campaign")

	if _, _, err := st.SetNodePortrait(ctx, otherCampaign, bart.ID, "t/x/node/a/portrait"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign write: err = %v, want ErrNotFound", err)
	}
	if _, _, err := st.SetNodePortrait(ctx, campaignID, uuid.New(), "t/x/node/a/portrait"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing node: err = %v, want ErrNotFound", err)
	}
}

// TestDeleteNode_ReturnsThePortraitKey: after the DELETE nothing in the
// database names the bytes, so the key must come back for the seam release.
func TestDeleteNode_ReturnsThePortraitKey(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	if _, _, err := st.SetNodePortrait(ctx, campaignID, bart.ID, "t/x/node/a/portrait"); err != nil {
		t.Fatalf("SetNodePortrait: %v", err)
	}

	key, err := st.DeleteNode(ctx, campaignID, bart.ID)
	if err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if key != "t/x/node/a/portrait" {
		t.Errorf("DeleteNode returned key %q, want the portrait key", key)
	}
}

// TestListCampaignPortraitKeys: the campaign hard delete's sweep input — every
// non-empty key, and only this campaign's.
func TestListCampaignPortraitKeys(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	mira := mkNode(t, st, campaignID, storage.KGNodeNPC, "Mira")
	mkNode(t, st, campaignID, storage.KGNodeNPC, "No portrait")
	for n, key := range map[uuid.UUID]string{
		bart.ID: "t/x/node/a/portrait",
		mira.ID: "t/x/node/b/portrait",
	} {
		if _, _, err := st.SetNodePortrait(ctx, campaignID, n, key); err != nil {
			t.Fatalf("SetNodePortrait: %v", err)
		}
	}
	// Another campaign's portrait must not enter this campaign's sweep.
	otherCampaign := insertCampaign(t, st, tenantID, "Second campaign")
	foreign := mkNode(t, st, otherCampaign, storage.KGNodeNPC, "Foreign")
	if _, _, err := st.SetNodePortrait(ctx, otherCampaign, foreign.ID, "t/x/node/f/portrait"); err != nil {
		t.Fatalf("SetNodePortrait foreign: %v", err)
	}

	keys, err := st.ListCampaignPortraitKeys(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListCampaignPortraitKeys: %v", err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	if len(keys) != 2 || !got["t/x/node/a/portrait"] || !got["t/x/node/b/portrait"] {
		t.Errorf("keys = %v, want exactly this campaign's two", keys)
	}
}

// TestPortraitSeedContext_PublicOnly: the read that seeds a generated
// portrait's prompt (#590) — public Aspects only, and a gm_private entry has no
// public depiction to generate (the MapSeedContext posture).
func TestPortraitSeedContext_PublicOnly(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart, err := st.CreateNodeWithAspects(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "Bart",
		Body: "The innkeeper.",
	}, []storage.NewKGNodeAspect{
		{Key: "appearance", Value: "red beard"},
		{Key: "secret", Value: "owes the guild", GMPrivate: true},
	})
	if err != nil {
		t.Fatalf("CreateNodeWithAspects: %v", err)
	}

	node, err := st.PortraitSeedContext(ctx, campaignID, bart.ID)
	if err != nil {
		t.Fatalf("PortraitSeedContext: %v", err)
	}
	if node.Name != "Bart" || node.Body != "The innkeeper." {
		t.Fatalf("seed came back wrong: %+v", node)
	}
	if len(node.Aspects) != 1 || node.Aspects[0].Key != "appearance" {
		t.Fatalf("aspects = %+v; a gm_private fact leaked or a public one was lost", node.Aspects)
	}

	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "The spymaster", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	if _, err := st.PortraitSeedContext(ctx, campaignID, secret.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a gm_private entry: err = %v, want ErrNotFound", err)
	}
}
