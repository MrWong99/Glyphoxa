package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestListSupportedLanguages proves the handler reports the Campaign Language
// choices as the registered phonetic-encoder codes, sorted (ADR-0024). The
// registry is the sole language-truth source, so the shipped en/de matrix comes
// back as ["de","en"] with no store read and no auth needed — an all-nil
// CampaignStores composition suffices (#445).
func TestListSupportedLanguages(t *testing.T) {
	t.Parallel()
	client := campaignClientAs(t, rpc.CampaignStores{}, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.ListSupportedLanguages(context.Background(),
		connect.NewRequest(&managementv1.ListSupportedLanguagesRequest{}))
	if err != nil {
		t.Fatalf("ListSupportedLanguages: %v", err)
	}
	got := resp.Msg.GetLanguages()
	want := []string{"de", "en"}
	if len(got) != len(want) {
		t.Fatalf("languages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("languages = %v, want %v", got, want)
		}
	}
}
