package wirenpc

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestLoadCampaignNPCs_NilCampaignFailsLoud is #323 decision 3: an empty
// CampaignID in the runtime path is a caller bug (the loop config never received
// the selected campaign), never a silent fall back to the seed roster. The check
// returns BEFORE any query, so this is a Docker-free unit test — no container —
// and it asserts the actionable "empty campaign id" message, not just non-nil.
func TestLoadCampaignNPCs_NilCampaignFailsLoud(t *testing.T) {
	_, _, _, err := loadCampaignNPCs(context.Background(), storage.New(nil), uuid.Nil)
	if err == nil {
		t.Fatal("loadCampaignNPCs(uuid.Nil) returned nil error; want a loud refusal")
	}
	if !strings.Contains(err.Error(), "empty campaign id") {
		t.Errorf("error %q does not name the empty-campaign-id cause", err.Error())
	}
}
