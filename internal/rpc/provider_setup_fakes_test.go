package rpc

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The two providerStore fakes of this package's other tests never exercise
// the Setup checklist writers (guild name, insert-only activation, voice LLM
// removal); these methods only keep them satisfying the interface.

func (f *fakeInviteStore) SaveDiscordGuildName(context.Context, uuid.UUID, string, string) (bool, error) {
	return false, nil
}

func (f *fakeInviteStore) InsertKeylessProviderConfig(context.Context, storage.NewProviderConfig) (storage.ProviderConfig, bool, error) {
	return storage.ProviderConfig{}, false, nil
}

func (f *fakeInviteStore) DeleteTenantLLMConfigs(context.Context, uuid.UUID, []string) (int64, error) {
	return 0, nil
}

func (stubProviderStore) SaveDiscordGuildName(context.Context, uuid.UUID, string, string) (bool, error) {
	return false, nil
}

func (stubProviderStore) InsertKeylessProviderConfig(context.Context, storage.NewProviderConfig) (storage.ProviderConfig, bool, error) {
	return storage.ProviderConfig{}, false, nil
}

func (stubProviderStore) DeleteTenantLLMConfigs(context.Context, uuid.UUID, []string) (int64, error) {
	return 0, nil
}

// SetGuildProofNamedForTest overrides the #504 guild-admin proof seam with a
// checker that also returns the guild name stored with the link.
func (s *ProviderServer) SetGuildProofNamedForTest(fn func(ctx context.Context, token, guildID, userID string) (string, error)) {
	s.checkGuildAdmin = fn
}

// SetClockForTest pins the clock behind GetUsageSummary's resets_at.
func (s *ProviderServer) SetClockForTest(now func() time.Time) {
	s.now = now
}
