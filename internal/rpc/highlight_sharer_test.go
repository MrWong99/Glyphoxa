package rpc_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

// TestDeploymentSharer_NoTokenPaths pins that every token-less shape — no saved
// token AND no env fallback — is ErrNoDiscordToken (the RPC renders it as
// "save a Discord Bot token first", now accurate advice): no deployment row,
// the ENV placeholder, and an unsaved empty last4, each with no env token set.
func TestDeploymentSharer_NoTokenPaths(t *testing.T) {
	cipher := testCipher(t)

	cases := map[string]struct {
		deps   fakeDeploymentReader
		cipher *crypto.Cipher
	}{
		"no deployment row": {fakeDeploymentReader{err: storage.ErrNotFound}, cipher},
		"env placeholder":   {fakeDeploymentReader{dep: storage.DeploymentConfig{DiscordBotTokenLast4: "env"}}, cipher},
		"unsaved (empty)":   {fakeDeploymentReader{dep: storage.DeploymentConfig{DiscordBotTokenLast4: ""}}, cipher},
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

// TestDeploymentSharer_SavedTokenWithoutCipherIsHardError pins the wirenpc AC3
// posture: a REAL saved token that cannot be decrypted (no $GLYPHOXA_SECRET) is
// a CLEAR error — never ErrNoDiscordToken ("save a token first" would be wrong:
// one IS saved) and never a silent env fall-through.
func TestDeploymentSharer_SavedTokenWithoutCipherIsHardError(t *testing.T) {
	deps := fakeDeploymentReader{dep: storage.DeploymentConfig{
		DiscordBotTokenLast4:      "abcd",
		DiscordBotTokenCiphertext: []byte("x"),
		GuildID:                   "guild-42",
	}}
	s := rpc.NewDeploymentSharer(deps, nil, slog.Default())
	s.SetEnvBotToken("central-env-token") // must NOT be silently used

	_, err := s.ListTextChannels(shareCtx())
	if err == nil || errors.Is(err, rpc.ErrNoDiscordToken) {
		t.Fatalf("ListTextChannels err = %v, want a hard decrypt error (not nil, not ErrNoDiscordToken)", err)
	}
	if !strings.Contains(err.Error(), "GLYPHOXA_SECRET") {
		t.Fatalf("ListTextChannels err = %v, want the $GLYPHOXA_SECRET hint", err)
	}
}

// TestDeploymentSharer_EnvTokenFallback is the ADR-0057 central-token case the
// production incident hit: a Tenant with a linked guild and NO saved per-tenant
// Bot token (empty last4 / the seed's "env" placeholder / no row at all) must
// list channels and post clips with the deployment's central env token — the
// SAME ladder its session starts already resolve — instead of dead-ending on
// "save a Discord Bot token first" while Start works.
func TestDeploymentSharer_EnvTokenFallback(t *testing.T) {
	cipher := testCipher(t)
	for name, dep := range map[string]fakeDeploymentReader{
		"empty last4":     {dep: storage.DeploymentConfig{GuildID: "guild-42"}},
		"env placeholder": {dep: storage.DeploymentConfig{GuildID: "guild-42", DiscordBotTokenLast4: "env"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := rpc.NewDeploymentSharer(dep, cipher, slog.Default())
			s.SetEnvBotToken("central-env-token")

			var gotToken, gotGuild, gotVoiceToken, gotVoiceGuild, gotPostToken string
			s.SetShareSeamsForTest(
				func(_ context.Context, token, guildID string, _ *slog.Logger) ([]discordshare.Channel, error) {
					gotToken, gotGuild = token, guildID
					return []discordshare.Channel{{ID: "1", Name: "general"}}, nil
				},
				func(_ context.Context, token, _, _, _, _ string, _ []byte, _ *slog.Logger) error {
					gotPostToken = token
					return nil
				},
			)
			s.SetVoiceListSeamForTest(func(_ context.Context, token, guildID string, _ *slog.Logger) ([]discordshare.Channel, error) {
				gotVoiceToken, gotVoiceGuild = token, guildID
				return []discordshare.Channel{{ID: "2", Name: "Tavern"}}, nil
			})

			if _, err := s.ListTextChannels(shareCtx()); err != nil {
				t.Fatalf("ListTextChannels: %v", err)
			}
			if gotToken != "central-env-token" || gotGuild != "guild-42" {
				t.Fatalf("text list got token %q guild %q, want the env token + linked guild", gotToken, gotGuild)
			}
			if _, err := s.ListVoiceChannels(shareCtx()); err != nil {
				t.Fatalf("ListVoiceChannels: %v", err)
			}
			if gotVoiceToken != "central-env-token" || gotVoiceGuild != "guild-42" {
				t.Fatalf("voice list got token %q guild %q, want the env token + linked guild", gotVoiceToken, gotVoiceGuild)
			}
			if err := s.PostClip(shareCtx(), "c", "cap", "highlight.wav", "audio/wav", []byte("x")); err != nil {
				t.Fatalf("PostClip: %v", err)
			}
			if gotPostToken != "central-env-token" {
				t.Fatalf("post got token %q, want the env token", gotPostToken)
			}
		})
	}
}

// TestDeploymentSharer_EnvTokenWithoutGuildIsNoGuildLinked: with the env rung a
// token resolves for a Tenant that never linked a server, so the LIST paths
// must answer that state with ErrNoGuildLinked ("link a Discord server first"),
// not a misleading token error and not a raw empty-guild transport failure.
func TestDeploymentSharer_EnvTokenWithoutGuildIsNoGuildLinked(t *testing.T) {
	for name, dep := range map[string]fakeDeploymentReader{
		"no deployment row": {err: storage.ErrNotFound},
		"row without guild": {dep: storage.DeploymentConfig{}},
	} {
		t.Run(name, func(t *testing.T) {
			s := rpc.NewDeploymentSharer(dep, testCipher(t), slog.Default())
			s.SetEnvBotToken("central-env-token")
			if _, err := s.ListTextChannels(shareCtx()); !errors.Is(err, rpc.ErrNoGuildLinked) {
				t.Fatalf("ListTextChannels err = %v, want ErrNoGuildLinked", err)
			}
			if _, err := s.ListVoiceChannels(shareCtx()); !errors.Is(err, rpc.ErrNoGuildLinked) {
				t.Fatalf("ListVoiceChannels err = %v, want ErrNoGuildLinked", err)
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
