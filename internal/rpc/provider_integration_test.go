//go:build integration

// Drives ProviderService against a real *storage.Store (testcontainers Postgres)
// end to end over Connect-JSON, with the app cipher, proving the write-only BYOK
// contract at the storage boundary: a saved secret is sealed at rest and never
// returned. Tag-isolated behind `integration` (ADR-0021 / ADR-0033). The
// Postgres harness (startPostgres) is shared with campaign_integration_test.go.

package rpc_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// seedTenant migrates the schema and inserts one tenant, returning a Store over
// the DB, the pool (for direct assertions), and the tenant id.
func seedTenant(t *testing.T, dsn string) (*storage.Store, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ('Acme TTRPG') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return storage.New(pool), pool, tenantID
}

func providerClient(t *testing.T, store *storage.Store, cipher *crypto.Cipher, tenantID uuid.UUID) managementv1connect.ProviderServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// The #504 guild-admin proof needs an authenticated saver identity.
			ctx = auth.WithUser(auth.WithTenant(ctx, tenantID),
				storage.User{ID: uuid.New(), DiscordUserID: testSaverDiscordID})
			return next(ctx, req)
		}
	})
	srvImpl := rpc.NewProviderServer(store, cipher, nil)
	// Always-pass proof stub: this suite exercises the STORAGE boundary; the
	// live Discord proof stays behind its seam (ADR-0033).
	srvImpl.SetGuildProofForTest(func(context.Context, string, string, string) error { return nil })
	mux := http.NewServeMux()
	mux.Handle(srvImpl.Handler(connect.WithInterceptors(inject)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewProviderServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

func TestProviderService_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, pool, tenantID := seedTenant(t, dsn)
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	client := providerClient(t, store, cipher, tenantID)
	ctx := context.Background()

	const secret = "test-groq-secret-aaaa"

	// Save a Groq key.
	saveResp, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: secret, Model: "llama-3.3-70b-versatile",
	}))
	if err != nil {
		t.Fatalf("SaveProviderConfig: %v", err)
	}
	if got := saveResp.Msg.GetCredential().GetLast4(); got != crypto.Last4(secret) {
		t.Errorf("last4 = %q, want %q", got, crypto.Last4(secret))
	}
	if strings.Contains(saveResp.Msg.GetCredential().String(), secret) {
		t.Fatal("response leaked the plaintext secret")
	}

	// At rest the DB holds ciphertext (sealed), never the plaintext, and it opens
	// back to the original — the write-only round-trip.
	var ciphertext []byte
	var last4 string
	if err := pool.QueryRow(ctx,
		`SELECT credentials_ciphertext, credentials_last4 FROM provider_config
		  WHERE tenant_id = $1 AND component = 'llm' AND provider = 'groq'`, tenantID).
		Scan(&ciphertext, &last4); err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	if strings.Contains(string(ciphertext), secret) {
		t.Fatal("stored ciphertext contains the plaintext")
	}
	opened, err := cipher.Open(ciphertext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != secret {
		t.Errorf("round-trip = %q, want %q", opened, secret)
	}
	if last4 != crypto.Last4(secret) {
		t.Errorf("stored last4 = %q, want %q", last4, crypto.Last4(secret))
	}

	// Reload shows masked metadata, no value.
	list, err := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	groq := credByProvider(list.Msg.GetCredentials(), "groq")
	if groq == nil || !groq.GetShowMasked() || groq.GetLast4() != crypto.Last4(secret) {
		t.Errorf("reload groq = %+v, want masked with last4", groq)
	}
	if groq.GetUpdatedAt() == nil {
		t.Error("reload groq missing updated_at")
	}

	// Replace updates last4 + timestamp.
	const secret2 = "test-groq-secret-rotated-bbbb"
	replaceResp, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: secret2,
	}))
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaceResp.Msg.GetCredential().GetLast4() != crypto.Last4(secret2) {
		t.Errorf("replace last4 = %q, want %q", replaceResp.Msg.GetCredential().GetLast4(), crypto.Last4(secret2))
	}
	if !replaceResp.Msg.GetCredential().GetUpdatedAt().AsTime().After(groq.GetUpdatedAt().AsTime()) {
		t.Error("replace did not advance updated_at")
	}

	// ElevenLabs save writes both stt + tts rows from one key.
	if _, err := client.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "elevenlabs", Secret: "test-elevenlabs-secret-cccc",
	})); err != nil {
		t.Fatalf("elevenlabs save: %v", err)
	}
	var elevenRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM provider_config WHERE tenant_id = $1 AND provider = 'elevenlabs'`, tenantID).
		Scan(&elevenRows); err != nil {
		t.Fatalf("count elevenlabs: %v", err)
	}
	if elevenRows != 2 {
		t.Errorf("elevenlabs rows = %d, want 2 (stt + tts)", elevenRows)
	}

	// Discord settings: token sealed in deployment_config, IDs stored plain.
	const botToken = "test-discord-bot-token-dddd"
	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		BotToken: &[]string{botToken}[0], GuildId: strPtr("472093001100"), VoiceChannelId: strPtr("472093774421"),
	})); err != nil {
		t.Fatalf("SaveDiscordSettings: %v", err)
	}
	var depCiphertext []byte
	var depLast4, guildID, voiceID string
	if err := pool.QueryRow(ctx,
		`SELECT discord_bot_token_ciphertext, discord_bot_token_last4, guild_id, voice_channel_id
		   FROM deployment_config WHERE tenant_id = $1`, tenantID).
		Scan(&depCiphertext, &depLast4, &guildID, &voiceID); err != nil {
		t.Fatalf("read deployment_config: %v", err)
	}
	if strings.Contains(string(depCiphertext), botToken) {
		t.Fatal("deployment_config stores the plaintext bot token")
	}
	if depLast4 != crypto.Last4(botToken) || guildID != "472093001100" || voiceID != "472093774421" {
		t.Errorf("deployment row = last4 %q guild %q voice %q", depLast4, guildID, voiceID)
	}

	final, _ := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if final.Msg.GetGuildId() != "472093001100" || final.Msg.GetVoiceChannelId() != "472093774421" {
		t.Errorf("list ids = %q / %q", final.Msg.GetGuildId(), final.Msg.GetVoiceChannelId())
	}
	if d := credByProvider(final.Msg.GetCredentials(), "discord"); d == nil || !d.GetEverSaved() {
		t.Errorf("discord slot not saved: %+v", d)
	}
}
