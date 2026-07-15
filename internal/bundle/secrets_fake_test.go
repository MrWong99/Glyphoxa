package bundle_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
)

// TestSecretsExclusionPropertyFake is the DB-free migration of the ADR-0053 §2
// property test (#451). With the store seam in place, the heart of the
// property is STRUCTURAL: [bundle.ExportStore] carries no method that reads
// provider_config, deployment_config, users, or auth sessions, so ciphertext
// can never reach Export at all — that end of the original test needs no
// database to hold. What remains to guard here is the seam types themselves:
// rows the seam DOES read carry secret-adjacent fields (an Agent's provider
// FK ids, a Character's linked_user_id, a chunk's embedding_model), and none
// of them may reach bundle bytes. Distinctive markers are planted in each
// such field and the gunzipped output of both export variants is scanned —
// alongside the schema words whose appearance would betray a forbidden field
// joining the wire format.
//
// The markers are deliberately NON-hex-shaped (like the integration test's)
// where they are free strings, and the provider FKs are full UUIDs matched by
// their complete string form, so neither can collide with the legitimate UUID
// refs a bundle carries. The integration TestSecretsExclusionProperty keeps
// the true-ciphertext seeding against real tables.
func TestSecretsExclusionPropertyFake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	f := newFakeStore()
	campaignID, _ := seedFakeCampaign(t, f)

	// Plant markers in every secret-adjacent field the seam types expose.
	voiceProviderFK := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	llmProviderFK := uuid.MustParse("ffffffff-0000-1111-2222-333333333333")
	const linkedUserMarker = "linked-user-zqxw-marker"
	const embedModelMarker = "text-embed-wqzx-marker"
	for i := range f.agents {
		f.agents[i].VoiceProviderConfigID = uuid.NullUUID{UUID: voiceProviderFK, Valid: true}
		f.agents[i].LLMProviderConfigID = uuid.NullUUID{UUID: llmProviderFK, Valid: true}
		f.agents[i].SpeakerColor = 7
	}
	for i := range f.characters {
		marker := linkedUserMarker
		f.characters[i].LinkedUserID = &marker
	}
	for i := range f.chunks {
		f.chunks[i].EmbeddingModel = embedModelMarker
	}

	forbidden := [][]byte{
		[]byte(voiceProviderFK.String()),
		[]byte(llmProviderFK.String()),
		[]byte(linkedUserMarker),
		[]byte(embedModelMarker),
		// Schema words a leaked field would carry onto the wire.
		[]byte("ciphertext"),
		[]byte("last4"),
		[]byte("credentials"),
		[]byte("provider_config"),
		[]byte("deployment_config"),
		[]byte("linked_user_id"),
		[]byte("embedding"),
		[]byte("speaker_color"),
	}

	for _, tc := range []struct {
		name string
		opts bundle.ExportOptions
	}{
		{"no-history", bundle.ExportOptions{}},
		{"history", bundle.ExportOptions{IncludeHistory: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := bundle.Export(ctx, f, campaignID, tc.opts)
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
			for _, want := range forbidden {
				if bytes.Contains(plain, want) {
					t.Errorf("bundle leaked forbidden bytes %q", want)
				}
			}
		})
	}
}
