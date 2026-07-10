//go:build integration

package bundle_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// TestSecretsExclusionProperty is the ADR-0053 §2 named property test: NO secret
// byte reaches ANY bundle. It seeds a Campaign whose provider_config carries a
// distinctive real-looking ciphertext + a "ab12" last4, and whose
// deployment_config holds a distinctive Bot-token ciphertext, then adds
// transcript history — and asserts the gunzipped bytes of BOTH the history and
// no-history exports contain none of that key material, nor the schema words a
// leak would carry. The property holds by construction (Export reads no secret
// table), so a regression here means someone added a forbidden read or field.
func TestSecretsExclusionProperty(t *testing.T) {
	ctx := context.Background()
	cipher := testCipher(t)
	pool := migratedPool(t)
	if err := wirenpc.SeedNPC(ctx, pool, cipher, nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	st := storage.New(pool)

	tenant, err := st.FindTenantByName(ctx, wirenpc.SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, wirenpc.SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	cid := campaign.ID

	// Distinctive marker bytes seeded into the secret tables. The base64 form of
	// the ciphertext is checked too, since JSON encoding of []byte is base64.
	const providerMarker = "SUPERSECRETCIPHERTEXT-ab12-marker"
	const deployMarker = "DEPLOYMENTBOTTOKENCIPHERTEXT-marker"
	const last4 = "ab12"

	provCipher, err := cipher.Seal([]byte(providerMarker))
	if err != nil {
		t.Fatalf("seal provider: %v", err)
	}
	if _, err := st.CreateProviderConfig(ctx, storage.NewProviderConfig{
		TenantID:              tenant.ID,
		Component:             storage.ComponentLLM,
		Provider:              "openai",
		Model:                 "gpt-4o",
		CredentialsCiphertext: provCipher,
		CredentialsLast4:      last4,
	}); err != nil {
		t.Fatalf("CreateProviderConfig: %v", err)
	}
	deployCipher, err := cipher.Seal([]byte(deployMarker))
	if err != nil {
		t.Fatalf("seal deploy: %v", err)
	}
	if _, err := st.SaveDiscordBotToken(ctx, tenant.ID, deployCipher, "9999"); err != nil {
		t.Fatalf("SaveDiscordBotToken: %v", err)
	}

	// Transcript history so the property is checked on a history bundle too.
	vs, err := st.CreateVoiceSession(ctx, cid)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: cid, LineID: "l1", Seq: 1,
		Who: "Frodo", Kind: "human", TS: time.Now().UTC(), Text: "hello",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine: %v", err)
	}
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: cid, VoiceSessionID: vs.ID, Content: "hello", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}

	forbidden := [][]byte{
		provCipher,
		[]byte(base64.StdEncoding.EncodeToString(provCipher)),
		deployCipher,
		[]byte(base64.StdEncoding.EncodeToString(deployCipher)),
		[]byte(providerMarker),
		[]byte(deployMarker),
		[]byte(last4),
		[]byte("ciphertext"),
		[]byte("last4"),
		[]byte("credentials"),
		[]byte("deployment_config"),
	}

	for _, tc := range []struct {
		name string
		opts bundle.ExportOptions
	}{
		{"no-history", bundle.ExportOptions{}},
		{"history", bundle.ExportOptions{IncludeHistory: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := bundle.Export(ctx, st, cid, tc.opts)
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			var buf bytes.Buffer
			if err := bundle.Encode(&buf, b); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			gz, err := gzip.NewReader(&buf)
			if err != nil {
				t.Fatalf("gzip reader: %v", err)
			}
			plain, err := io.ReadAll(gz)
			if err != nil {
				t.Fatalf("gunzip: %v", err)
			}
			for _, f := range forbidden {
				if len(f) == 0 {
					continue
				}
				if bytes.Contains(plain, f) {
					t.Errorf("bundle leaked forbidden bytes %q", f)
				}
			}
		})
	}
}
