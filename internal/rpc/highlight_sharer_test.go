package rpc_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

type fakeDeploymentReader struct {
	dep storage.DeploymentConfig
	err error
}

func (f fakeDeploymentReader) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	return f.dep, f.err
}

// shareCtx carries a resolved Tenant so the tenant-scoped resolve (#489) finds
// one — the share path runs behind the auth stack in production.
func shareCtx() context.Context {
	return auth.WithTenant(context.Background(), uuid.New())
}

// TestDeploymentSharer_NoTokenPaths pins that every unsaved-token shape is
// ErrNoDiscordToken (the RPC renders it as "save a Discord Bot token first"): no
// deployment row, the ENV placeholder, and a nil cipher.
func TestDeploymentSharer_NoTokenPaths(t *testing.T) {
	cipher := testCipher(t)

	cases := map[string]struct {
		deps   fakeDeploymentReader
		cipher *crypto.Cipher
	}{
		"no deployment row": {fakeDeploymentReader{err: storage.ErrNotFound}, cipher},
		"env placeholder":   {fakeDeploymentReader{dep: storage.DeploymentConfig{DiscordBotTokenLast4: "env"}}, cipher},
		"unsaved (empty)":   {fakeDeploymentReader{dep: storage.DeploymentConfig{DiscordBotTokenLast4: ""}}, cipher},
		"no cipher":         {fakeDeploymentReader{dep: storage.DeploymentConfig{DiscordBotTokenLast4: "abcd", DiscordBotTokenCiphertext: []byte("x")}}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := rpc.NewDeploymentSharer(c.deps, c.cipher, slog.Default())
			if _, err := s.ListTextChannels(shareCtx()); !errors.Is(err, rpc.ErrNoDiscordToken) {
				t.Fatalf("ListTextChannels err = %v, want ErrNoDiscordToken", err)
			}
			if err := s.PostClip(shareCtx(), "c", "cap", "highlight.wav", "audio/wav", []byte("x")); !errors.Is(err, rpc.ErrNoDiscordToken) {
				t.Fatalf("PostClip err = %v, want ErrNoDiscordToken", err)
			}
		})
	}
}

// TestDeploymentSharer_ResolvesTokenAndCalls pins that a saved token is decrypted
// and the guild id + token flow into the Discord seams.
func TestDeploymentSharer_ResolvesTokenAndCalls(t *testing.T) {
	cipher := testCipher(t)
	sealed, err := cipher.Seal([]byte("bot-token-xyz"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	deps := fakeDeploymentReader{dep: storage.DeploymentConfig{
		DiscordBotTokenLast4:      "-xyz",
		DiscordBotTokenCiphertext: sealed,
		GuildID:                   "guild-42",
	}}
	s := rpc.NewDeploymentSharer(deps, cipher, slog.Default())

	var gotToken, gotGuild, gotChannel string
	var gotData []byte
	s.SetShareSeamsForTest(
		func(_ context.Context, token, guildID string, _ *slog.Logger) ([]discordshare.Channel, error) {
			gotToken, gotGuild = token, guildID
			return []discordshare.Channel{{ID: "1", Name: "general"}}, nil
		},
		func(_ context.Context, token, channelID, _, _, _ string, data []byte, _ *slog.Logger) error {
			gotToken, gotChannel, gotData = token, channelID, data
			return nil
		},
	)

	chs, err := s.ListTextChannels(shareCtx())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotToken != "bot-token-xyz" || gotGuild != "guild-42" {
		t.Fatalf("list resolved token/guild = %q/%q, want bot-token-xyz/guild-42", gotToken, gotGuild)
	}
	if len(chs) != 1 || chs[0].Name != "general" {
		t.Fatalf("channels = %+v", chs)
	}

	if err := s.PostClip(shareCtx(), "chan9", "cap", "highlight.wav", "audio/wav", []byte("WAV")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotToken != "bot-token-xyz" || gotChannel != "chan9" || string(gotData) != "WAV" {
		t.Fatalf("post got token=%q channel=%q data=%q", gotToken, gotChannel, gotData)
	}
}
