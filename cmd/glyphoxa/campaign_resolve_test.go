package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeStandaloneResolver stubs the two-step standalone Active-Campaign resolution
// so the ordering (durable → recent → actionable error) is pinned without a DB.
type fakeStandaloneResolver struct {
	durable    storage.Campaign
	durableErr error
	recent     storage.Campaign
	recentErr  error
}

func (f fakeStandaloneResolver) GetOperatorActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.durable, f.durableErr
}
func (f fakeStandaloneResolver) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.recent, f.recentErr
}

// TestResolveStandaloneCampaign_DurableWinsOverRecent is the #323 orchestrator
// ruling: the standalone voice node mirrors the web tier's durable→recent policy,
// so the operator's /glyphoxa use selection outranks the most-recently-created
// campaign — the two surfaces voice the SAME campaign.
func TestResolveStandaloneCampaign_DurableWinsOverRecent(t *testing.T) {
	durable := storage.Campaign{ID: uuid.New(), Name: "LostMine"}
	recent := storage.Campaign{ID: uuid.New(), Name: "NewCampaign"}
	got, err := resolveStandaloneCampaign(context.Background(), fakeStandaloneResolver{
		durable: durable,
		recent:  recent,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != durable.ID {
		t.Errorf("resolved %q, want durable selection %q (durable must outrank recent)", got.Name, durable.Name)
	}
}

// TestResolveStandaloneCampaign_FallsBackToRecent: no durable selection
// (ErrNotFound) falls through to the most-recently-created campaign — a fresh
// install that never ran /glyphoxa use still resolves.
func TestResolveStandaloneCampaign_FallsBackToRecent(t *testing.T) {
	recent := storage.Campaign{ID: uuid.New(), Name: "OnlyCampaign"}
	got, err := resolveStandaloneCampaign(context.Background(), fakeStandaloneResolver{
		durableErr: storage.ErrNotFound,
		recent:     recent,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != recent.ID {
		t.Errorf("resolved %q, want recent fallback %q", got.Name, recent.Name)
	}
}

// TestResolveStandaloneCampaign_NothingResolvableIsActionable: an empty DB (no
// durable, no campaign at all) fails LOUDLY with an actionable message, never the
// old silent seed-roster / raw pg error.
func TestResolveStandaloneCampaign_NothingResolvableIsActionable(t *testing.T) {
	_, err := resolveStandaloneCampaign(context.Background(), fakeStandaloneResolver{
		durableErr: storage.ErrNotFound,
		recentErr:  storage.ErrNotFound,
	})
	if err == nil {
		t.Fatal("resolve returned nil on an empty DB; want an actionable no-campaign error")
	}
	for _, want := range []string{"no Active Campaign", "glyphoxa seed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing actionable hint %q", err.Error(), want)
		}
	}
}
