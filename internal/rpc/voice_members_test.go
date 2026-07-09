package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/disgoorg/snowflake/v2"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/presence"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

// voiceMembersClient builds a CampaignService client whose server has an optional
// member lister wired (nil = no standing presence). The Players panel picker
// (#279) reads ListDiscordVoiceMembers, which must degrade to an empty list — not
// an error — whenever the Bot can't answer, so the UI falls back to free-text.
func voiceMembersClient(
	t *testing.T,
	lister func(ctx context.Context) ([]presence.Member, error),
) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServer(newFakeStore())
	if lister != nil {
		srv.SetMemberLister(lister)
	}
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, s.URL, connect.WithProtoJSON(),
	)
}

// A nil lister (no standing presence) or a lister failure (offline Bot) yields an
// empty list and NO error — the picker degrades to free-text snowflake entry.
func TestListDiscordVoiceMembers_BotOffline_EmptyNoError(t *testing.T) {
	t.Parallel()

	t.Run("nil lister", func(t *testing.T) {
		t.Parallel()
		client := voiceMembersClient(t, nil)
		resp, err := client.ListDiscordVoiceMembers(context.Background(),
			connect.NewRequest(&managementv1.ListDiscordVoiceMembersRequest{}))
		if err != nil {
			t.Fatalf("ListDiscordVoiceMembers: unexpected error %v", err)
		}
		if got := resp.Msg.GetMembers(); len(got) != 0 {
			t.Errorf("members = %v, want empty", got)
		}
	})

	t.Run("lister error", func(t *testing.T) {
		t.Parallel()
		client := voiceMembersClient(t, func(context.Context) ([]presence.Member, error) {
			return nil, errors.New("bot offline")
		})
		resp, err := client.ListDiscordVoiceMembers(context.Background(),
			connect.NewRequest(&managementv1.ListDiscordVoiceMembersRequest{}))
		if err != nil {
			t.Fatalf("ListDiscordVoiceMembers: unexpected error %v", err)
		}
		if got := resp.Msg.GetMembers(); len(got) != 0 {
			t.Errorf("members = %v, want empty", got)
		}
	})
}

// A lister returning members maps each onto the wire type: snowflake id as a
// decimal string, display name, and avatar url.
func TestListDiscordVoiceMembers_MapsMembers(t *testing.T) {
	t.Parallel()

	client := voiceMembersClient(t, func(context.Context) ([]presence.Member, error) {
		return []presence.Member{
			{ID: snowflake.ID(111111111111111111), DisplayName: "Aravel", AvatarURL: "https://cdn/a.png"},
			{ID: snowflake.ID(222222222222222222), DisplayName: "Borin", AvatarURL: ""},
		}, nil
	})

	resp, err := client.ListDiscordVoiceMembers(context.Background(),
		connect.NewRequest(&managementv1.ListDiscordVoiceMembersRequest{}))
	if err != nil {
		t.Fatalf("ListDiscordVoiceMembers: %v", err)
	}
	got := resp.Msg.GetMembers()
	if len(got) != 2 {
		t.Fatalf("members len = %d, want 2", len(got))
	}
	if got[0].GetDiscordUserId() != "111111111111111111" {
		t.Errorf("member[0] id = %q, want 111111111111111111", got[0].GetDiscordUserId())
	}
	if got[0].GetDisplayName() != "Aravel" {
		t.Errorf("member[0] display_name = %q, want Aravel", got[0].GetDisplayName())
	}
	if got[0].GetAvatarUrl() != "https://cdn/a.png" {
		t.Errorf("member[0] avatar_url = %q", got[0].GetAvatarUrl())
	}
	if got[1].GetDiscordUserId() != "222222222222222222" {
		t.Errorf("member[1] id = %q, want 222222222222222222", got[1].GetDiscordUserId())
	}
}
