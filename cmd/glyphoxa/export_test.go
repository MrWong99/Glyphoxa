package main

import (
	"context"
	"strings"
	"testing"
)

// TestRunExport_MissingCampaign is TEST 6 (arg parse): a missing -campaign fails
// fast with an actionable error and never touches the DB.
func TestRunExport_MissingCampaign(t *testing.T) {
	err := RunExport(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "-campaign") {
		t.Fatalf("missing -campaign: err=%v, want mention of -campaign", err)
	}
}

// TestRunExport_BadCampaignUUID rejects a non-UUID -campaign before opening the DB.
func TestRunExport_BadCampaignUUID(t *testing.T) {
	err := RunExport(context.Background(), []string{"-campaign", "not-a-uuid"})
	if err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("bad uuid: err=%v, want UUID complaint", err)
	}
}
