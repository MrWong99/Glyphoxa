package rpc_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

// TestProviderPresenceRefresher pins #102: a successful SaveDiscordSettings fires
// the presence refresher (so the new token/Guild registers the slash-command
// surface without a restart), and a REJECTED save does not.
func TestProviderPresenceRefresher(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	fired := make(chan uuid.UUID, 4)
	srv.SetPresenceRefresher(func(id uuid.UUID) { fired <- id })
	client, tenantID := clientForServer(t, srv)
	ctx := context.Background()

	// Successful save (token + channels) → refresher fires.
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken:       strPtr("some-discord-bot-token-1234"),
		GuildId:        strPtr("472093001100"),
		VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("save: %v", err)
	}
	select {
	case got := <-fired:
		if got != tenantID {
			t.Fatalf("refresher fired for tenant %s, want the saving tenant %s (#489: only that tenant refreshes)", got, tenantID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("presence refresher did not fire after a successful save")
	}

	// Rejected save (present-but-empty IDs) → refresher must NOT fire.
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		GuildId: strPtr(""), VoiceChannelId: strPtr(""),
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty IDs code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	select {
	case <-fired:
		t.Fatal("presence refresher fired after a rejected save")
	case <-time.After(200 * time.Millisecond):
	}
}
