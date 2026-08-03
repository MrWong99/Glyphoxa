package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestFindDuplicateEntries pins the world health panel's duplicate check (#536):
// pairs map through closest-first, the campaign is scoped server-side, and the
// unembedded count rides along so "no duplicates" cannot imply a clean bill of
// health the scan could not actually give.
func TestFindDuplicateEntries(t *testing.T) {
	t.Parallel()
	store := newFakeKGGraphStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Saltmarsh"}
	a, b := uuid.New(), uuid.New()
	store.pairs = []storage.KGNodePair{
		{AID: a, AName: "Bart", BID: b, BName: "Bart the innkeeper", Similarity: 0.97},
	}
	store.unembedded = 3

	resp, err := kgGraphClient(t, store).FindDuplicateEntries(context.Background(),
		connect.NewRequest(&managementv1.FindDuplicateEntriesRequest{}))
	if err != nil {
		t.Fatalf("FindDuplicateEntries: %v", err)
	}
	got := resp.Msg.GetPairs()
	if len(got) != 1 {
		t.Fatalf("got %d pairs, want 1", len(got))
	}
	if got[0].GetAName() != "Bart" || got[0].GetBName() != "Bart the innkeeper" {
		t.Errorf("pair = %+v, want both names mapped", got[0])
	}
	if got[0].GetSimilarity() < 0.9 {
		t.Errorf("similarity = %v, want the stored value", got[0].GetSimilarity())
	}
	if resp.Msg.GetUnembedded() != 3 {
		t.Errorf("unembedded = %d, want 3 — a partial scan must say so", resp.Msg.GetUnembedded())
	}
	if store.pairCampaign != store.campaign.ID {
		t.Errorf("scan scoped to %s, want the active campaign %s", store.pairCampaign, store.campaign.ID)
	}
	// A high floor is the difference between a hint the GM reads and a wall of
	// coincidences they learn to ignore.
	if store.pairFloor < 0.85 {
		t.Errorf("similarity floor = %v, want a high floor so hints stay trustworthy", store.pairFloor)
	}
	if store.pairLimit <= 0 {
		t.Errorf("pair limit = %d, want a cap", store.pairLimit)
	}
}

// TestFindDuplicateEntries_NoDuplicates pins the healthy case as an empty list,
// not an error.
func TestFindDuplicateEntries_NoDuplicates(t *testing.T) {
	t.Parallel()
	store := newFakeKGGraphStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	resp, err := kgGraphClient(t, store).FindDuplicateEntries(context.Background(),
		connect.NewRequest(&managementv1.FindDuplicateEntriesRequest{}))
	if err != nil {
		t.Fatalf("FindDuplicateEntries: %v", err)
	}
	if len(resp.Msg.GetPairs()) != 0 {
		t.Errorf("got %+v, want no pairs", resp.Msg.GetPairs())
	}
}

func TestFindDuplicateEntries_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("no campaign is NotFound", func(t *testing.T) {
		store := newFakeKGGraphStore()
		store.campErr = storage.ErrNotFound
		_, err := kgGraphClient(t, store).FindDuplicateEntries(context.Background(),
			connect.NewRequest(&managementv1.FindDuplicateEntriesRequest{}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("scan failure is Internal", func(t *testing.T) {
		store := newFakeKGGraphStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.pairErr = errAny
		_, err := kgGraphClient(t, store).FindDuplicateEntries(context.Background(),
			connect.NewRequest(&managementv1.FindDuplicateEntriesRequest{}))
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want Internal", got)
		}
	})
}
