//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestMigration00059_MovesElevenLabsV3VoicesToV4Turbo pins the #702 data move:
// every ElevenLabs agent voice still pinned to eleven_v3 (the pre-#702 default
// every first save and 00016 wrote explicitly) moves to eleven_v4_turbo with the
// rest of its Settings intact; a voice on another model, a non-ElevenLabs voice
// and a {} no-voice row are left alone. Down moves the rewritten voices back.
// Applies the schema up to just before 00059, seeds raw rows, then applies 00059
// (the 00016 / 00058 pattern).
func TestMigration00059_MovesElevenLabsV3VoicesToV4Turbo(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 58); err != nil {
		t.Fatalf("migrate up to 00058: %v", err)
	}

	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Voice Model Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Model Campaign", System: "dnd5e", Language: "de",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	agent := func(name, voice string) string {
		t.Helper()
		id, err := st.CreateAgent(ctx, storage.NewAgent{
			CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: name, Voice: []byte(voice),
		})
		if err != nil {
			t.Fatalf("CreateAgent(%s): %v", name, err)
		}
		return id.String()
	}
	const v3Voice = `{"ProviderID":"elevenlabs","VoiceID":"JBFqnCBsd6RMkjVDRZzb","Name":"Bart","Language":"de","Metadata":null,` +
		`"Settings":{"model_id":"eleven_v3","output_format":"pcm_48000","suggested_audio_tags":["whispers"],` +
		`"voice_settings":{"stability":0.5,"similarity_boost":0.75,"use_speaker_boost":true}}}`
	moved := agent("Bart", v3Voice)
	flash := agent("Flash", `{"ProviderID":"elevenlabs","VoiceID":"f","Settings":{"model_id":"eleven_flash_v2_5"}}`)
	other := agent("Other", `{"ProviderID":"someone-else","VoiceID":"o","Settings":{"model_id":"eleven_v3"}}`)
	empty := agent("NoVoice", `{}`)

	voice := func(id string) map[string]any {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT voice FROM agents WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatalf("read voice: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode voice %s: %v", raw, err)
		}
		return m
	}
	settings := func(id string) map[string]any {
		t.Helper()
		s, _ := voice(id)["Settings"].(map[string]any)
		return s
	}
	snapshot := func(id string) string {
		t.Helper()
		b, _ := json.Marshal(voice(id))
		return string(b)
	}
	flashBefore, otherBefore := snapshot(flash), snapshot(other)

	if _, err := provider.UpTo(ctx, 59); err != nil {
		t.Fatalf("migrate up to 00059: %v", err)
	}

	s := settings(moved)
	if s["model_id"] != "eleven_v4_turbo" {
		t.Errorf("v3 voice model_id = %v after 00059, want eleven_v4_turbo", s["model_id"])
	}
	if s["output_format"] != "pcm_48000" {
		t.Errorf("output_format = %v, want pcm_48000 kept", s["output_format"])
	}
	if vs, _ := s["voice_settings"].(map[string]any); vs["stability"] != 0.5 || vs["use_speaker_boost"] != true {
		t.Errorf("voice_settings = %v, want the saved values kept", s["voice_settings"])
	}
	if tags, _ := s["suggested_audio_tags"].([]any); len(tags) != 1 || tags[0] != "whispers" {
		t.Errorf("suggested_audio_tags = %v, want [whispers] kept", s["suggested_audio_tags"])
	}
	if v := voice(moved); v["VoiceID"] != "JBFqnCBsd6RMkjVDRZzb" || v["Name"] != "Bart" || v["Language"] != "de" {
		t.Errorf("voice identity changed: %v", v)
	}
	if got := snapshot(flash); got != flashBefore {
		t.Errorf("flash voice changed: %s, want %s", got, flashBefore)
	}
	if got := snapshot(other); got != otherBefore {
		t.Errorf("non-ElevenLabs voice changed: %s, want %s", got, otherBefore)
	}
	if got := snapshot(empty); got != "{}" {
		t.Errorf("no-voice row = %s, want {}", got)
	}

	if _, err := provider.DownTo(ctx, 58); err != nil {
		t.Fatalf("migrate down to 00058: %v", err)
	}
	if s := settings(moved); s["model_id"] != "eleven_v3" {
		t.Errorf("model_id after Down = %v, want eleven_v3 back", s["model_id"])
	}
	if got := snapshot(flash); got != flashBefore {
		t.Errorf("flash voice changed by Down: %s", got)
	}
}
