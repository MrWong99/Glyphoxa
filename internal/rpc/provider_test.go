package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// fakeProviderStore is an in-memory providerStore: it mimics the (tenant,
// component, provider) upsert and the column-isolated deployment_config saves so
// the handler can be unit-tested keyless.
type fakeProviderStore struct {
	configs     map[string]storage.ProviderConfig // key: component|provider
	dep         *storage.DeploymentConfig
	tick        int
	caps        storage.SpendCaps // per-Tenant spend caps (#130)
	capsSet     bool
	channelsErr error // scripted SaveDiscordChannels failure (#483 guild collision)

	// channelsCalls counts SaveDiscordChannels invocations so #504 tests assert
	// a failed guild-admin proof never reaches the binding write.
	channelsCalls int
}

func newFakeProviderStore() *fakeProviderStore {
	return &fakeProviderStore{configs: map[string]storage.ProviderConfig{}}
}

// now returns a strictly-increasing timestamp so updated_at advances on replace.
func (f *fakeProviderStore) now() time.Time {
	f.tick++
	return time.Date(2026, 6, 25, 12, 0, f.tick, 0, time.UTC)
}

func (f *fakeProviderStore) ListProviderConfigs(_ context.Context, _ uuid.UUID) ([]storage.ProviderConfig, error) {
	out := make([]storage.ProviderConfig, 0, len(f.configs))
	for _, c := range f.configs {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeProviderStore) GetProviderConfigByComponent(_ context.Context, _ uuid.UUID, component storage.Component) (storage.ProviderConfig, error) {
	for _, c := range f.configs {
		if c.Component == component {
			return c, nil
		}
	}
	return storage.ProviderConfig{}, storage.ErrNotFound
}

func (f *fakeProviderStore) UpsertProviderConfigs(_ context.Context, configs []storage.NewProviderConfig) ([]storage.ProviderConfig, error) {
	out := make([]storage.ProviderConfig, 0, len(configs))
	for _, n := range configs {
		key := string(n.Component) + "|" + n.Provider
		row, ok := f.configs[key]
		if !ok {
			row = storage.ProviderConfig{ID: uuid.New(), TenantID: n.TenantID, Component: n.Component, Provider: n.Provider, CreatedAt: f.now()}
		}
		row.Model = n.Model
		row.CredentialsCiphertext = n.CredentialsCiphertext
		row.CredentialsLast4 = n.CredentialsLast4
		row.UpdatedAt = f.now()
		f.configs[key] = row
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeProviderStore) GetDeploymentConfig(_ context.Context, _ uuid.UUID) (storage.DeploymentConfig, error) {
	if f.dep == nil {
		return storage.DeploymentConfig{}, storage.ErrNotFound
	}
	return *f.dep, nil
}

func (f *fakeProviderStore) SaveDiscordBotToken(_ context.Context, tenantID uuid.UUID, ciphertext []byte, last4 string) (storage.DeploymentConfig, error) {
	if f.dep == nil {
		f.dep = &storage.DeploymentConfig{TenantID: tenantID, CreatedAt: f.now()}
	}
	f.dep.DiscordBotTokenCiphertext = ciphertext
	f.dep.DiscordBotTokenLast4 = last4
	f.dep.UpdatedAt = f.now()
	return *f.dep, nil
}

func (f *fakeProviderStore) SaveDiscordChannels(_ context.Context, tenantID uuid.UUID, guildID, voiceChannelID string) (storage.DeploymentConfig, error) {
	f.channelsCalls++
	if f.channelsErr != nil {
		return storage.DeploymentConfig{}, f.channelsErr
	}
	if f.dep == nil {
		f.dep = &storage.DeploymentConfig{TenantID: tenantID, CreatedAt: f.now()}
	}
	f.dep.GuildID = guildID
	f.dep.VoiceChannelID = voiceChannelID
	f.dep.UpdatedAt = f.now()
	return *f.dep, nil
}

// ReleaseDiscordGuild mirrors the storage compare-and-clear (#504): only a
// matching tenant-held binding clears; anything else is ErrNotFound.
func (f *fakeProviderStore) ReleaseDiscordGuild(_ context.Context, _ uuid.UUID, guildID string) (storage.DeploymentConfig, error) {
	if f.dep == nil || f.dep.GuildID == "" || f.dep.GuildID != guildID {
		return storage.DeploymentConfig{}, storage.ErrNotFound
	}
	f.dep.GuildID = ""
	f.dep.VoiceChannelID = ""
	f.dep.UpdatedAt = f.now()
	return *f.dep, nil
}

func (f *fakeProviderStore) GetTenantSpendCaps(context.Context, uuid.UUID) (storage.SpendCaps, error) {
	if !f.capsSet {
		return storage.SpendCaps{}, nil
	}
	return f.caps, nil
}

func (f *fakeProviderStore) SetTenantSpendCaps(_ context.Context, _ uuid.UUID, caps storage.SpendCaps) error {
	f.caps = caps
	f.capsSet = true
	return nil
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// newProviderClient mounts a ProviderServer behind a server-side interceptor
// that injects a fixed tenant (the auth stack's job, faked here), and returns a
// Connect-JSON client. WithProtoJSON also asserts the RPCs work over JSON.
func newProviderClient(t *testing.T, store *fakeProviderStore, cipher *crypto.Cipher) (managementv1connect.ProviderServiceClient, uuid.UUID) {
	t.Helper()
	return clientForServer(t, rpc.NewProviderServer(store, cipher, nil))
}

// testSaverDiscordID is the Discord snowflake of the fixed operator the test
// interceptor injects — the saver identity the #504 guild-admin proof checks.
const testSaverDiscordID = "555000000000000000"

// clientForServer mounts a PRE-BUILT ProviderServer behind the fixed-tenant +
// fixed-user interceptor and returns a Connect-JSON client + the injected
// tenant. Callers that must configure the server before it serves (e.g.
// SetDiscordApplicationID, #110) build it and pass it in; newProviderClient is
// the common store+cipher shortcut over this.
//
// The guild-admin proof (#504) is stubbed to always-pass so pre-#504 ID-save
// tests stay green and never dial Discord; tests exercising the proof override
// it via SetGuildProofForTest AFTER this call (requests only start later).
func clientForServer(t *testing.T, server *rpc.ProviderServer) (managementv1connect.ProviderServiceClient, uuid.UUID) {
	t.Helper()
	server.SetGuildProofForTest(func(context.Context, string, string, string) error { return nil })
	return mountProviderServer(t, server, true)
}

// clientForServerNoUser mounts a server whose interceptor injects a tenant but
// NO authenticated user — the #504 missing-principal path.
func clientForServerNoUser(t *testing.T, server *rpc.ProviderServer) (managementv1connect.ProviderServiceClient, uuid.UUID) {
	t.Helper()
	server.SetGuildProofForTest(func(context.Context, string, string, string) error { return nil })
	return mountProviderServer(t, server, false)
}

func mountProviderServer(t *testing.T, server *rpc.ProviderServer, withUser bool) (managementv1connect.ProviderServiceClient, uuid.UUID) {
	t.Helper()
	tenantID := uuid.New()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			if withUser {
				ctx = auth.WithUser(ctx, storage.User{ID: uuid.New(), DiscordUserID: testSaverDiscordID})
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(server.Handler(connect.WithInterceptors(inject)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewProviderServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON()), tenantID
}

// TestProviderList_IntegrationStatus pins #489: the Configuration read surfaces
// THIS tenant's standing Discord client health from the per-tenant registry —
// "failed" with the classified detail after a terminal token death — so a
// BYOK-token revocation is visible to the tenant. Without a source wired (web-only
// mode) it reads empty.
func TestProviderList_IntegrationStatus(t *testing.T) {
	t.Parallel()

	srv := rpc.NewProviderServer(newFakeProviderStore(), testCipher(t), nil)
	srv.SetIntegrationStatusSource(func(_ uuid.UUID) (string, string) {
		return "failed", "invalid_bot_token: gateway rejected identify (close 4004)"
	})
	client, _ := clientForServer(t, srv)

	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := resp.Msg.GetIntegrationState(); got != "failed" {
		t.Errorf("integration_state = %q, want failed", got)
	}
	if got := resp.Msg.GetIntegrationDetail(); got == "" {
		t.Errorf("integration_detail = empty, want the classified reason")
	}

	// No source wired (web-only) reads empty.
	bare := rpc.NewProviderServer(newFakeProviderStore(), testCipher(t), nil)
	bareClient, _ := clientForServer(t, bare)
	bareResp, err := bareClient.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List (bare): %v", err)
	}
	if got := bareResp.Msg.GetIntegrationState(); got != "" {
		t.Errorf("integration_state without a source = %q, want empty", got)
	}
}

// credByProvider finds a credential in a list by its provider slot.
func credByProvider(creds []*managementv1.ProviderCredential, provider string) *managementv1.ProviderCredential {
	for _, c := range creds {
		if c.GetProvider() == provider {
			return c
		}
	}
	return nil
}

// TestProviderList_DiscordApplicationID pins #110: the configured Discord
// application (client) id — the same app that backs operator login (ADR-0016) —
// is exposed on the read so the SPA composes the bot-authorization URL without
// hardcoding anything. Without the setter wired it reads empty, the fallback the
// SPA renders as a disabled action + note.
func TestProviderList_DiscordApplicationID(t *testing.T) {
	t.Parallel()

	srv := rpc.NewProviderServer(newFakeProviderStore(), testCipher(t), nil)
	srv.SetDiscordApplicationID("123456789012345678")
	client, _ := clientForServer(t, srv)

	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := resp.Msg.GetDiscordApplicationId(); got != "123456789012345678" {
		t.Errorf("discord_application_id = %q, want 123456789012345678", got)
	}

	// No setter → empty on the wire (the missing-app-id fallback).
	bare, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	resp2, err := bare.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List (bare): %v", err)
	}
	if got := resp2.Msg.GetDiscordApplicationId(); got != "" {
		t.Errorf("discord_application_id without setter = %q, want empty", got)
	}
}

func TestProviderList_EmptyShowsKeyNeeded(t *testing.T) {
	t.Parallel()
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))

	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	creds := resp.Msg.GetCredentials()
	if len(creds) != 4 {
		t.Fatalf("credentials = %d, want 4 (discord, groq, elevenlabs, gemini)", len(creds))
	}
	for _, want := range []string{"discord", "groq", "elevenlabs", "gemini"} {
		c := credByProvider(creds, want)
		if c == nil {
			t.Fatalf("missing credential slot %q", want)
		}
		if c.GetEverSaved() || c.GetShowMasked() || c.GetLast4() != "" {
			t.Errorf("%s should be key-needed on an empty store: %+v", want, c)
		}
	}
	if resp.Msg.GetGuildId() != "" || resp.Msg.GetVoiceChannelId() != "" {
		t.Errorf("guild/voice should be empty: %q / %q", resp.Msg.GetGuildId(), resp.Msg.GetVoiceChannelId())
	}
}

func TestProviderSave_SealStoreLast4_WriteOnly(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	cipher := testCipher(t)
	client, _ := newProviderClient(t, store, cipher)

	const secret = "test-groq-secret-value-1111"
	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq",
		Secret:   secret,
		Model:    "llama-3.3-70b-versatile",
	}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	cred := resp.Msg.GetCredential()
	if !cred.GetEverSaved() || !cred.GetShowMasked() {
		t.Errorf("saved credential should be ever_saved + show_masked: %+v", cred)
	}
	if cred.GetComponent() != "llm" || cred.GetProvider() != "groq" {
		t.Errorf("component/provider = %q/%q, want llm/groq", cred.GetComponent(), cred.GetProvider())
	}
	if cred.GetLast4() != crypto.Last4(secret) {
		t.Errorf("last4 = %q, want %q", cred.GetLast4(), crypto.Last4(secret))
	}

	// Write-only contract: the plaintext must not appear anywhere in the response.
	if strings.Contains(cred.String(), secret) {
		t.Fatal("response leaked the plaintext secret")
	}

	// Seal → store round-trip: the stored ciphertext opens back to the plaintext,
	// proving the handler sealed it (and stored ciphertext, never plaintext).
	stored := store.configs["llm|groq"]
	if string(stored.CredentialsCiphertext) == secret {
		t.Fatal("stored ciphertext is the raw plaintext")
	}
	opened, err := cipher.Open(stored.CredentialsCiphertext)
	if err != nil {
		t.Fatalf("open stored ciphertext: %v", err)
	}
	if string(opened) != secret {
		t.Errorf("round-trip = %q, want %q", opened, secret)
	}
}

func TestProviderSave_GeminiUpsertsImage(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	cipher := testCipher(t)
	client, _ := newProviderClient(t, store, cipher)

	const secret = "test-gemini-secret-3333"
	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "gemini", Secret: secret,
	}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	cred := resp.Msg.GetCredential()
	if cred.GetComponent() != "image" || cred.GetProvider() != "gemini" {
		t.Errorf("component/provider = %q/%q, want image/gemini", cred.GetComponent(), cred.GetProvider())
	}
	// The image Component row was upserted with the sealed key.
	row, ok := store.configs["image|gemini"]
	if !ok {
		t.Fatalf("missing upserted image|gemini row")
	}
	if row.CredentialsLast4 != crypto.Last4(secret) {
		t.Errorf("image last4 = %q, want %q", row.CredentialsLast4, crypto.Last4(secret))
	}
}

func TestProviderSave_ElevenLabsUpsertsSttAndTts(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	cipher := testCipher(t)
	client, _ := newProviderClient(t, store, cipher)

	const secret = "test-elevenlabs-secret-2222"
	if _, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "elevenlabs", Secret: secret,
	})); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// One save wrote both Components, sharing the ciphertext + last4.
	for _, comp := range []string{"tts|elevenlabs", "stt|elevenlabs"} {
		row, ok := store.configs[comp]
		if !ok {
			t.Fatalf("missing upserted row %q", comp)
		}
		if row.CredentialsLast4 != crypto.Last4(secret) {
			t.Errorf("%s last4 = %q, want %q", comp, row.CredentialsLast4, crypto.Last4(secret))
		}
	}

	// List collapses the two rows into one ElevenLabs slot.
	resp, _ := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	c := credByProvider(resp.Msg.GetCredentials(), "elevenlabs")
	if c == nil || !c.GetEverSaved() || c.GetComponent() != "tts" {
		t.Errorf("elevenlabs slot = %+v, want one saved tts-labelled credential", c)
	}
}

func TestProviderSave_ReplaceUpdatesLast4AndTimestamp(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client, _ := newProviderClient(t, store, testCipher(t))
	ctx := context.Background()

	first, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "first_keyAAAA"}))
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "second_keyBBBB"}))
	if err != nil {
		t.Fatalf("replace save: %v", err)
	}

	if first.Msg.GetCredential().GetLast4() == second.Msg.GetCredential().GetLast4() {
		t.Errorf("replace did not change last4 (%q)", second.Msg.GetCredential().GetLast4())
	}
	if !second.Msg.GetCredential().GetUpdatedAt().AsTime().After(first.Msg.GetCredential().GetUpdatedAt().AsTime()) {
		t.Errorf("replace did not advance updated_at: %v !> %v",
			second.Msg.GetCredential().GetUpdatedAt().AsTime(), first.Msg.GetCredential().GetUpdatedAt().AsTime())
	}
}

func TestProviderSave_UnknownProviderAndEmptySecret(t *testing.T) {
	t.Parallel()
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	ctx := context.Background()

	_, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "openai", Secret: "x"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown provider code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	_, err = client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: ""}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty secret code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestProviderSave_NoCipherFailsPrecondition(t *testing.T) {
	t.Parallel()
	client, _ := newProviderClient(t, newFakeProviderStore(), nil) // nil cipher
	ctx := context.Background()

	_, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "x"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("save without cipher code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	// Reads still work without a cipher.
	if _, err := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{})); err != nil {
		t.Errorf("List without cipher: %v", err)
	}
}

func TestProviderDiscordSettings_TokenAndChannels(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client, _ := newProviderClient(t, store, testCipher(t))
	ctx := context.Background()

	const token = "test-discord-bot-token-3333"
	saveTok, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken:       &[]string{token}[0],
		GuildId:        strPtr("472093001100"),
		VoiceChannelId: strPtr("472093774421"),
	}))
	if err != nil {
		t.Fatalf("save discord: %v", err)
	}
	cred := saveTok.Msg.GetCredential()
	if !cred.GetEverSaved() || cred.GetProvider() != "discord" || cred.GetLast4() != crypto.Last4(token) {
		t.Errorf("discord credential = %+v, want saved/discord/last4=%q", cred, crypto.Last4(token))
	}
	if strings.Contains(cred.String(), token) {
		t.Fatal("discord response leaked the bot token")
	}
	if saveTok.Msg.GetGuildId() != "472093001100" || saveTok.Msg.GetVoiceChannelId() != "472093774421" {
		t.Errorf("ids not stored: %q / %q", saveTok.Msg.GetGuildId(), saveTok.Msg.GetVoiceChannelId())
	}

	// IDs-only save (no bot_token) must not wipe the token.
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		GuildId: strPtr("999"), VoiceChannelId: strPtr("888"),
	})); err != nil {
		t.Fatalf("save ids: %v", err)
	}
	resp, _ := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	discord := credByProvider(resp.Msg.GetCredentials(), "discord")
	if discord == nil || !discord.GetEverSaved() || discord.GetLast4() != crypto.Last4(token) {
		t.Errorf("token wiped by ids-only save: %+v", discord)
	}
	if resp.Msg.GetGuildId() != "999" || resp.Msg.GetVoiceChannelId() != "888" {
		t.Errorf("ids not updated: %q / %q", resp.Msg.GetGuildId(), resp.Msg.GetVoiceChannelId())
	}
}

// TestProviderDiscordSettings_GuildOwnedByOtherTenant pins the #483 guild-collision
// fix (full guild-permission proof is #504): saving a guild_id already bound by a
// DIFFERENT Tenant (storage.ErrGuildTaken from the first-registrar-wins unique
// index) must surface as CodeFailedPrecondition with an actionable message — never
// silently rebind the victim's guild (a cross-tenant PII leak: the newest-wins read
// would hand the attacker the victim's voice-channel members + command routing).
func TestProviderDiscordSettings_GuildOwnedByOtherTenant(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	store.channelsErr = storage.ErrGuildTaken
	client, _ := newProviderClient(t, store, testCipher(t))

	// Since #504 the guild-admin proof runs first; the always-pass test stub
	// stands in for a proven admin, and the request token feeds the check —
	// ErrGuildTaken must still surface for a PROVEN admin of a taken guild.
	_, err := client.SaveDiscordSettings(context.Background(), connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-3333"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("save owned guild code = %v (err %v), want CodeFailedPrecondition", connect.CodeOf(err), err)
	}
	if err == nil || !strings.Contains(err.Error(), "another tenant") {
		t.Errorf("save owned guild err = %v, want a message naming the other-tenant binding", err)
	}
}

// TestProviderDiscordSettings_TokenOnlySaveKeepsIDs pins #142: replacing the
// bot token without sending the IDs must leave the stored Guild / Voice channel
// IDs untouched — the exact clobber that wiped them when the client saved a
// token while ListProviderConfigs was still loading.
func TestProviderDiscordSettings_TokenOnlySaveKeepsIDs(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client, _ := newProviderClient(t, store, testCipher(t))
	ctx := context.Background()

	// Operator has a token + IDs saved (the token also feeds the #504 proof's
	// check-token ladder).
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-0000"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("save ids: %v", err)
	}

	// Token-only save: no ID fields on the wire.
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-7777"),
	})); err != nil {
		t.Fatalf("save token: %v", err)
	}

	resp, err := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resp.Msg.GetGuildId() != "472093001100" || resp.Msg.GetVoiceChannelId() != "472093774421" {
		t.Errorf("token-only save clobbered ids: guild=%q voice=%q, want them untouched",
			resp.Msg.GetGuildId(), resp.Msg.GetVoiceChannelId())
	}
}

// TestProviderDiscordSettings_EmptyIDsRejected documents the #142 decision for
// present-but-empty IDs: REJECT with InvalidArgument (mirroring bot_token's
// "must not be empty when provided") rather than treating "" as an explicit
// clear. Clearing the IDs is not a supported operation — an empty ID only ever
// reaches the wire by accident (e.g. the form saving before the config load
// resolves), and accepting it would reopen the silent-wipe this issue fixes.
func TestProviderDiscordSettings_EmptyIDsRejected(t *testing.T) {
	t.Parallel()
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	ctx := context.Background()

	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: strPtr("test-discord-bot-token-0000"),
		GuildId:  strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("save ids: %v", err)
	}

	for name, req := range map[string]*managementv1.SaveDiscordSettingsRequest{
		"both empty":  {GuildId: strPtr(""), VoiceChannelId: strPtr("")},
		"empty guild": {GuildId: strPtr(""), VoiceChannelId: strPtr("472093774421")},
		"empty voice": {GuildId: strPtr("472093001100"), VoiceChannelId: strPtr("")},
		"guild only":  {GuildId: strPtr("472093001100")}, // partial presence = the other ID is empty
	} {
		_, err := client.SaveDiscordSettings(ctx, connect.NewRequest(req))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, connect.CodeOf(err))
		}
	}

	// The rejected saves left the stored IDs untouched.
	resp, err := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resp.Msg.GetGuildId() != "472093001100" || resp.Msg.GetVoiceChannelId() != "472093774421" {
		t.Errorf("rejected save mutated ids: guild=%q voice=%q", resp.Msg.GetGuildId(), resp.Msg.GetVoiceChannelId())
	}
}

func strPtr(s string) *string { return &s }

// TestProviderList_EnvPlaceholderIsKeyNeeded asserts the ADR-0039 seam: a
// provider_config still holding the seed's "env" placeholder reads as key-needed,
// not as a saved key.
func TestProviderList_EnvPlaceholderIsKeyNeeded(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	store.configs["llm|groq"] = storage.ProviderConfig{
		ID: uuid.New(), Component: storage.ComponentLLM, Provider: "groq",
		CredentialsLast4: "env", UpdatedAt: time.Now(),
	}
	client, _ := newProviderClient(t, store, testCipher(t))

	resp, _ := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	groq := credByProvider(resp.Msg.GetCredentials(), "groq")
	if groq.GetEverSaved() {
		t.Errorf("env-placeholder groq should be key-needed, got ever_saved=true: %+v", groq)
	}
}

// TestProviderSave_ModelOnlyKeepsSecret pins the #227 model-only branch: an
// empty secret with a model updates the stored model and leaves the sealed
// credential byte-identical. The second client runs WITHOUT a cipher to prove
// the model-only path never needs one (nothing is sealed).
func TestProviderSave_ModelOnlyKeepsSecret(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	seedClient, _ := newProviderClient(t, store, testCipher(t))
	if _, err := seedClient.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "sk-groq-secret-abcd", Model: "old-model",
	})); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	before, err := store.GetProviderConfigByComponent(context.Background(), uuid.Nil, storage.ComponentLLM)
	if err != nil {
		t.Fatalf("read seeded row: %v", err)
	}

	client, _ := newProviderClient(t, store, nil) // nil cipher: model-only must not need it
	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "", Model: "custom-model-x",
	}))
	if err != nil {
		t.Fatalf("model-only save: %v", err)
	}
	cred := resp.Msg.GetCredential()
	if cred.GetModel() != "custom-model-x" || !cred.GetEverSaved() || cred.GetLast4() != "abcd" {
		t.Errorf("credential after model-only save = model %q, everSaved %v, last4 %q; want custom-model-x, true, abcd",
			cred.GetModel(), cred.GetEverSaved(), cred.GetLast4())
	}
	after, err := store.GetProviderConfigByComponent(context.Background(), uuid.Nil, storage.ComponentLLM)
	if err != nil {
		t.Fatalf("read updated row: %v", err)
	}
	if after.Model != "custom-model-x" {
		t.Errorf("stored model = %q, want custom-model-x", after.Model)
	}
	if string(after.CredentialsCiphertext) != string(before.CredentialsCiphertext) || after.CredentialsLast4 != before.CredentialsLast4 {
		t.Error("model-only save must leave the sealed credential byte-identical")
	}
}

// TestProviderSave_ModelOnlyWithoutKeyRejected: a model alone cannot create a
// credential slot — with no stored row the empty-secret save is rejected
// exactly like before #227.
func TestProviderSave_ModelOnlyWithoutKeyRejected(t *testing.T) {
	t.Parallel()
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	_, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "", Model: "some-model",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("model-only save without stored key = %v, want CodeInvalidArgument", err)
	}
}

// TestProviderSave_ModelOnlyEmptyModelIsNoOp: empty secret + empty model reads
// the slot back without writing (such a request only reaches the wire by
// accident — mirrors #142's posture on empty Discord IDs).
func TestProviderSave_ModelOnlyEmptyModelIsNoOp(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client, _ := newProviderClient(t, store, testCipher(t))
	if _, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "sk-groq-secret-abcd", Model: "old-model",
	})); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	before, _ := store.GetProviderConfigByComponent(context.Background(), uuid.Nil, storage.ComponentLLM)

	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "", Model: "",
	}))
	if err != nil {
		t.Fatalf("empty model-only save: %v", err)
	}
	if got := resp.Msg.GetCredential().GetModel(); got != "old-model" {
		t.Errorf("no-op save returned model %q, want old-model", got)
	}
	after, _ := store.GetProviderConfigByComponent(context.Background(), uuid.Nil, storage.ComponentLLM)
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.Model != before.Model {
		t.Error("empty model-only save must not write")
	}
}
