package rpc_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/discordguild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// proofRecorder captures every checkGuildAdmin invocation so tests assert the
// checker receives the resolved token + guild + the saver's Discord snowflake.
type proofRecorder struct {
	mu    sync.Mutex
	calls []proofCall
	err   error
}

type proofCall struct{ token, guildID, userID string }

func (p *proofRecorder) check(_ context.Context, token, guildID, userID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, proofCall{token: token, guildID: guildID, userID: userID})
	return p.err
}

func (p *proofRecorder) snapshot() []proofCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proofCall(nil), p.calls...)
}

// TestProviderDiscordSettings_ProofReceivesSaverIdentity pins the #504 proof
// wiring: an IDs save invokes the guild-admin checker with the resolved Bot
// token (here the request's plaintext), the guild being bound, and the
// authenticated saver's Discord snowflake (auth.CurrentUser — never an env
// allowlist, ADR-0055).
func TestProviderDiscordSettings_ProofReceivesSaverIdentity(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	rec := &proofRecorder{}
	client, _ := clientForServer(t, srv)
	srv.SetGuildProofForTest(rec.check)

	const token = "test-discord-bot-token-3333"
	if _, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr(token),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("save: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("checker called %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.token != token {
		t.Errorf("checker token = %q, want the request plaintext", got.token)
	}
	if got.guildID != "472093001100" {
		t.Errorf("checker guild = %q, want 472093001100", got.guildID)
	}
	if got.userID != testSaverDiscordID {
		t.Errorf("checker user = %q, want the saver's snowflake %q", got.userID, testSaverDiscordID)
	}
}

// TestProviderDiscordSettings_SquatterRejected is the #504 squat regression: a
// saver who cannot prove guild administration (ErrNoPermission) gets
// PermissionDenied and the binding write NEVER happens — a squat-first attacker
// cannot bind an unowned guild. ErrUserNotInGuild collapses to the same single
// message (no membership-probing oracle).
func TestProviderDiscordSettings_SquatterRejected(t *testing.T) {
	t.Parallel()
	for name, proofErr := range map[string]error{
		"member without perms": discordguild.ErrNoPermission,
		"user not in guild":    discordguild.ErrUserNotInGuild,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeProviderStore()
			srv := rpc.NewProviderServer(store, testCipher(t), nil)
			client, _ := clientForServer(t, srv)
			srv.SetGuildProofForTest((&proofRecorder{err: proofErr}).check)

			_, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
				BotToken: strPtr("test-discord-bot-token-3333"),
				GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
			}))
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("code = %v (err %v), want PermissionDenied", connect.CodeOf(err), err)
			}
			if !strings.Contains(err.Error(), "Manage Server") {
				t.Errorf("err = %v, want the Manage Server message", err)
			}
			if store.channelsCalls != 0 {
				t.Errorf("SaveDiscordChannels called %d times after failed proof, want 0", store.channelsCalls)
			}
			// The proof runs BEFORE any write: the token save must not have
			// happened either (no partial write).
			if store.dep != nil {
				t.Errorf("rejected save mutated deployment_config: %+v", store.dep)
			}
		})
	}
}

// TestProviderDiscordSettings_BotNotInGuild: the Bot being unable to see the
// guild is FailedPrecondition "not a member" (403/404 collapse — no cross-guild
// existence leak), and nothing is written.
func TestProviderDiscordSettings_BotNotInGuild(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	client, _ := clientForServer(t, srv)
	srv.SetGuildProofForTest((&proofRecorder{err: discordguild.ErrBotNotInGuild}).check)

	_, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-3333"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v (err %v), want FailedPrecondition", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Errorf("err = %v, want the not-a-member message", err)
	}
	if store.dep != nil || store.channelsCalls != 0 {
		t.Errorf("rejected save wrote: dep=%+v channelsCalls=%d", store.dep, store.channelsCalls)
	}
}

// TestProviderDiscordSettings_ProofTokenResolution pins the check-token ladder
// (#504): request plaintext > stored BYOK token (decrypted) > central env token
// (SetEnvBotToken); all three empty is FailedPrecondition "save the ... token
// first" and the checker never runs.
func TestProviderDiscordSettings_ProofTokenResolution(t *testing.T) {
	t.Parallel()

	idsReq := func() *managementv1.SaveDiscordSettingsRequest {
		return &managementv1.SaveDiscordSettingsRequest{
			GuildId: strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
		}
	}

	t.Run("stored BYOK token decrypted", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore()
		cipher := testCipher(t)
		srv := rpc.NewProviderServer(store, cipher, nil)
		rec := &proofRecorder{}
		client, _ := clientForServer(t, srv)
		srv.SetGuildProofForTest(rec.check)

		const token = "test-discord-bot-token-7777"
		if _, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
			BotToken: strPtr(token),
			GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
		})); err != nil {
			t.Fatalf("seed token+ids: %v", err)
		}
		// IDs-only save: the stored sealed token is decrypted for the check.
		if _, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(idsReq())); err != nil {
			t.Fatalf("ids-only save: %v", err)
		}
		calls := rec.snapshot()
		if len(calls) != 2 || calls[1].token != token {
			t.Fatalf("checker calls = %+v, want 2nd with decrypted stored token", calls)
		}
	})

	t.Run("central env token", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore() // no deployment row at all
		srv := rpc.NewProviderServer(store, testCipher(t), nil)
		rec := &proofRecorder{}
		client, _ := clientForServer(t, srv)
		srv.SetGuildProofForTest(rec.check)
		srv.SetEnvBotToken("central-env-token")

		if _, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(idsReq())); err != nil {
			t.Fatalf("ids save: %v", err)
		}
		calls := rec.snapshot()
		if len(calls) != 1 || calls[0].token != "central-env-token" {
			t.Fatalf("checker calls = %+v, want the env token", calls)
		}
	})

	t.Run("no token anywhere", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore()
		srv := rpc.NewProviderServer(store, testCipher(t), nil)
		rec := &proofRecorder{}
		client, _ := clientForServer(t, srv)
		srv.SetGuildProofForTest(rec.check)

		_, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(idsReq()))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("code = %v (err %v), want FailedPrecondition", connect.CodeOf(err), err)
		}
		if !strings.Contains(err.Error(), "save the Discord bot token first") {
			t.Errorf("err = %v, want the save-token-first message", err)
		}
		if len(rec.snapshot()) != 0 {
			t.Error("checker ran without any token")
		}
	})
}

// TestProviderDiscordSettings_TokenOnlySaveSkipsProof: a token-only save binds
// no guild, so the proof is not invoked (#504).
func TestProviderDiscordSettings_TokenOnlySaveSkipsProof(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	rec := &proofRecorder{}
	client, _ := clientForServer(t, srv)
	srv.SetGuildProofForTest(rec.check)

	if _, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-3333"),
	})); err != nil {
		t.Fatalf("token-only save: %v", err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("checker called %d times on a token-only save, want 0", got)
	}
}

// TestProviderDiscordSettings_ProofRequiresSaverIdentity: an IDs save with no
// authenticated user in context (or one without a Discord snowflake) is
// Unauthenticated — the proof needs a saver identity to check (ADR-0055: the
// session principal, never an env allowlist).
func TestProviderDiscordSettings_ProofRequiresSaverIdentity(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetGuildProofForTest((&proofRecorder{}).check)
	client, _ := clientForServerNoUser(t, srv)

	_, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-3333"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v (err %v), want Unauthenticated", connect.CodeOf(err), err)
	}
	if store.dep != nil {
		t.Errorf("unauthenticated save wrote deployment_config: %+v", store.dep)
	}
}

// TestProviderReleaseDiscordGuild pins the #504 release RPC: echoing the bound
// guild clears the binding (response echoes empty IDs) and fires the
// invalidateHealth + refreshPresence hooks (the standing presence for the freed
// guild must tear down — critical for transfer); a mismatched echo is
// FailedPrecondition with NO hooks fired; an empty guild_id is InvalidArgument.
func TestProviderReleaseDiscordGuild(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetGuildProofForTest((&proofRecorder{}).check)

	var mu sync.Mutex
	var invalidated, refreshed []uuid.UUID
	refreshDone := make(chan struct{}, 8)
	srv.SetHealthInvalidator(func(id uuid.UUID) {
		mu.Lock()
		invalidated = append(invalidated, id)
		mu.Unlock()
	})
	srv.SetPresenceRefresher(func(id uuid.UUID) {
		mu.Lock()
		refreshed = append(refreshed, id)
		mu.Unlock()
		refreshDone <- struct{}{}
	})
	client, tenantID := clientForServer(t, srv)
	ctx := context.Background()

	// Empty guild_id → InvalidArgument.
	_, err := client.ReleaseDiscordGuild(ctx, connect.NewRequest(&managementv1.ReleaseDiscordGuildRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty guild_id code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	// Nothing bound yet → FailedPrecondition, no hooks.
	_, err = client.ReleaseDiscordGuild(ctx, connect.NewRequest(&managementv1.ReleaseDiscordGuildRequest{GuildId: "472093001100"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unbound release code = %v (err %v), want FailedPrecondition", connect.CodeOf(err), err)
	}

	// Bind, then release with the wrong echo → FailedPrecondition, binding stays.
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-3333"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Drain the save's own hook firings so the mismatch assertion below is clean.
	<-refreshDone
	mu.Lock()
	invalidated, refreshed = nil, nil
	mu.Unlock()

	_, err = client.ReleaseDiscordGuild(ctx, connect.NewRequest(&managementv1.ReleaseDiscordGuildRequest{GuildId: "999"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("mismatched release code = %v (err %v), want FailedPrecondition", connect.CodeOf(err), err)
	}
	mu.Lock()
	if len(invalidated) != 0 || len(refreshed) != 0 {
		t.Errorf("mismatched release fired hooks: invalidated=%v refreshed=%v", invalidated, refreshed)
	}
	mu.Unlock()
	if store.dep.GuildID != "472093001100" {
		t.Fatalf("mismatched release mutated binding: %q", store.dep.GuildID)
	}

	// Correct echo → cleared response + both hooks for THIS tenant.
	resp, err := client.ReleaseDiscordGuild(ctx, connect.NewRequest(&managementv1.ReleaseDiscordGuildRequest{GuildId: "472093001100"}))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if resp.Msg.GetGuildId() != "" || resp.Msg.GetVoiceChannelId() != "" {
		t.Errorf("release response = %q/%q, want both empty", resp.Msg.GetGuildId(), resp.Msg.GetVoiceChannelId())
	}
	<-refreshDone
	mu.Lock()
	defer mu.Unlock()
	if len(invalidated) != 1 || invalidated[0] != tenantID {
		t.Errorf("invalidateHealth = %v, want [%s]", invalidated, tenantID)
	}
	if len(refreshed) != 1 || refreshed[0] != tenantID {
		t.Errorf("refreshPresence = %v, want [%s]", refreshed, tenantID)
	}
	if store.dep.GuildID != "" || store.dep.VoiceChannelID != "" {
		t.Errorf("store after release = %q/%q, want cleared", store.dep.GuildID, store.dep.VoiceChannelID)
	}
	if store.dep.DiscordBotTokenLast4 != crypto.Last4("test-discord-bot-token-3333") {
		t.Errorf("release wiped the bot token: %q", store.dep.DiscordBotTokenLast4)
	}
}
