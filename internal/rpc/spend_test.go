package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func dptr(v float64) *float64 { return &v }

// TestSpendCaps_SetGetRoundTrip pins the Set→Get round-trip: both caps set, then
// clearing one back to unset (#130, ADR-0046). Absence (no field presence) is
// distinct from 0.
func TestSpendCaps_SetGetRoundTrip(t *testing.T) {
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	ctx := context.Background()

	// Default: neither cap.
	got, err := client.GetSpendCaps(ctx, connect.NewRequest(&managementv1.GetSpendCapsRequest{}))
	if err != nil {
		t.Fatalf("GetSpendCaps default: %v", err)
	}
	if got.Msg.GetCaps().SoftUsd != nil || got.Msg.GetCaps().HardUsd != nil {
		t.Fatalf("default caps = %+v, want both absent", got.Msg.GetCaps())
	}

	// Set both.
	set, err := client.SetSpendCaps(ctx, connect.NewRequest(&managementv1.SetSpendCapsRequest{
		SoftUsd: dptr(5), HardUsd: dptr(10),
	}))
	if err != nil {
		t.Fatalf("SetSpendCaps both: %v", err)
	}
	if set.Msg.GetCaps().GetSoftUsd() != 5 || set.Msg.GetCaps().GetHardUsd() != 10 {
		t.Fatalf("set echo = %+v, want soft=5 hard=10", set.Msg.GetCaps())
	}

	got, err = client.GetSpendCaps(ctx, connect.NewRequest(&managementv1.GetSpendCapsRequest{}))
	if err != nil {
		t.Fatalf("GetSpendCaps after set: %v", err)
	}
	if got.Msg.GetCaps().GetSoftUsd() != 5 || got.Msg.GetCaps().GetHardUsd() != 10 {
		t.Fatalf("caps after set = %+v, want soft=5 hard=10", got.Msg.GetCaps())
	}

	// Clear soft (omit it), keep hard.
	if _, err := client.SetSpendCaps(ctx, connect.NewRequest(&managementv1.SetSpendCapsRequest{HardUsd: dptr(10)})); err != nil {
		t.Fatalf("SetSpendCaps clear soft: %v", err)
	}
	got, _ = client.GetSpendCaps(ctx, connect.NewRequest(&managementv1.GetSpendCapsRequest{}))
	if got.Msg.GetCaps().SoftUsd != nil {
		t.Fatalf("soft after clear = %v, want absent", got.Msg.GetCaps().GetSoftUsd())
	}
	if got.Msg.GetCaps().GetHardUsd() != 10 {
		t.Fatalf("hard after clearing soft = %v, want 10", got.Msg.GetCaps().GetHardUsd())
	}
}

// TestSpendCaps_Validation pins the two InvalidArgument rules: a negative value and
// hard < soft (#130).
func TestSpendCaps_Validation(t *testing.T) {
	client, _ := newProviderClient(t, newFakeProviderStore(), testCipher(t))
	ctx := context.Background()

	cases := []struct {
		name string
		req  *managementv1.SetSpendCapsRequest
	}{
		{"negative soft", &managementv1.SetSpendCapsRequest{SoftUsd: dptr(-1)}},
		{"negative hard", &managementv1.SetSpendCapsRequest{HardUsd: dptr(-0.5)}},
		{"hard below soft", &managementv1.SetSpendCapsRequest{SoftUsd: dptr(10), HardUsd: dptr(5)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.SetSpendCaps(ctx, connect.NewRequest(tc.req))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
		})
	}

	// Equal is allowed (hard >= soft).
	if _, err := client.SetSpendCaps(ctx, connect.NewRequest(&managementv1.SetSpendCapsRequest{SoftUsd: dptr(5), HardUsd: dptr(5)})); err != nil {
		t.Fatalf("hard == soft must be allowed, got %v", err)
	}
}

// TestGetSession_CarriesSpendState pins that a live session's GetSession surfaces
// the meter's spend-cap state + estimated spend (#130, the reload truth for the
// Session screen badge).
func TestGetSession_CarriesSpendState(t *testing.T) {
	mgr := &fakeSessionManager{
		active:  true,
		current: storage.VoiceSession{ID: uuid.New(), Status: storage.VoiceSessionRunning},
		spend:   spend.Status{State: spend.CapSoft, EstimatedUSD: 7.5},
	}
	client := newSessionClient(t, mgr, &fakeSessionStore{})

	resp, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Msg.GetSpendCapState() != "soft" {
		t.Fatalf("spend_cap_state = %q, want soft", resp.Msg.GetSpendCapState())
	}
	if resp.Msg.GetEstimatedSpendUsd() != 7.5 {
		t.Fatalf("estimated_spend_usd = %v, want 7.5", resp.Msg.GetEstimatedSpendUsd())
	}
}
