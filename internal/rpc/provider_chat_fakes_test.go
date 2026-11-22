package rpc

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The two providerStore fakes of this package's other tests never exercise the
// Planning chat model choice; these methods only keep them satisfying the
// interface after it grew the chat writers (ADR-0062 amendment 2026-11-22).

func (f *fakeInviteStore) SaveChatLLMChoice(context.Context, storage.ChatLLMChoice) (storage.ProviderConfig, error) {
	return storage.ProviderConfig{}, nil
}

func (f *fakeInviteStore) DeleteChatLLMConfig(context.Context, uuid.UUID) error { return nil }

func (stubProviderStore) SaveChatLLMChoice(context.Context, storage.ChatLLMChoice) (storage.ProviderConfig, error) {
	return storage.ProviderConfig{}, nil
}

func (stubProviderStore) DeleteChatLLMConfig(context.Context, uuid.UUID) error { return nil }
