package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/presence"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// hungSeedStore is a presence.TenantStore whose boot seed never finishes until
// its ctx ends — standing in for a seed stuck on an unreachable Discord.
type hungSeedStore struct{ seeding chan struct{} }

func (h hungSeedStore) ListDeploymentConfigs(ctx context.Context) ([]storage.DeploymentConfig, error) {
	close(h.seeding)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (hungSeedStore) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, storage.ErrNotFound
}

func (hungSeedStore) GetTenantIDByGuildID(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, storage.ErrNotFound
}

func (hungSeedStore) GetCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return storage.Campaign{}, storage.ErrNotFound
}

// TestStartPresenceDoesNotWaitForTheSeed (#642): boot used to run the presence
// seed synchronously before the web listener bound (and before a worker's
// claim loop started), so a seed stuck on Discord kept /readyz down for the
// whole outage. startPresence must start the seed and return.
func TestStartPresenceDoesNotWaitForTheSeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := hungSeedStore{seeding: make(chan struct{})}
	clients := presence.NewClients(store, nil, presence.NewRegistry(nil, nil), "", nil)
	defer clients.Close()

	returned := make(chan struct{})
	go func() { defer close(returned); startPresence(ctx, clients) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("startPresence blocked on the presence seed; boot would not reach the web listener")
	}
	select {
	case <-store.seeding:
	case <-time.After(5 * time.Second):
		t.Fatal("startPresence never began the seed")
	}
}
