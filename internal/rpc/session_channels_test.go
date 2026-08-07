package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeChannelLister serves canned voice channels (or a scripted failure) for
// the ListSessionVoiceChannels seam, never touching the network.
type fakeChannelLister struct {
	channels []discordshare.Channel
	err      error
}

func (f *fakeChannelLister) ListVoiceChannels(context.Context) ([]discordshare.Channel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.channels, nil
}

// newChannelsClient mounts a SessionServer with the voice-channel seam wired
// (unless lister is nil — the unwired CodeUnimplemented posture) and an
// authenticated tenant.
func newChannelsClient(t *testing.T, lister *fakeChannelLister, deps *fakeDeploymentReader) managementv1connect.SessionServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithTenant(ctx, uuid.New()), req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, activeStore(), nil, nil)
	if lister != nil {
		srv.SetVoiceChannels(lister, deps)
	}
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	hsrv := httptest.NewServer(mux)
	t.Cleanup(hsrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, hsrv.URL, connect.WithProtoJSON())
}

// TestListSessionVoiceChannelsUnwired: a server without the seam reports
// CodeUnimplemented rather than panicking (the SetSharing posture).
func TestListSessionVoiceChannelsUnwired(t *testing.T) {
	t.Parallel()
	client := newChannelsClient(t, nil, nil)
	_, err := client.ListSessionVoiceChannels(context.Background(), connect.NewRequest(&managementv1.ListSessionVoiceChannelsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("unwired = %v, want CodeUnimplemented", err)
	}
}

// TestListSessionVoiceChannelsNoGuild: an unlinked guild (empty guild_id or no
// deployment row at all) is an actionable precondition, and the Discord lister
// is never consulted.
func TestListSessionVoiceChannelsNoGuild(t *testing.T) {
	t.Parallel()
	for name, deps := range map[string]*fakeDeploymentReader{
		"empty guild": {dep: storage.DeploymentConfig{}},
		"no row":      {err: storage.ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := newChannelsClient(t, &fakeChannelLister{err: errors.New("must not be called")}, deps)
			_, err := client.ListSessionVoiceChannels(context.Background(), connect.NewRequest(&managementv1.ListSessionVoiceChannelsRequest{}))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("no guild = %v, want CodeFailedPrecondition", err)
			}
		})
	}
}

// TestListSessionVoiceChannelsNoToken: a missing saved Bot token surfaces the
// same actionable precondition ListShareChannels uses.
func TestListSessionVoiceChannelsNoToken(t *testing.T) {
	t.Parallel()
	client := newChannelsClient(t,
		&fakeChannelLister{err: rpc.ErrNoDiscordToken},
		&fakeDeploymentReader{dep: storage.DeploymentConfig{GuildID: "g1"}})
	_, err := client.ListSessionVoiceChannels(context.Background(), connect.NewRequest(&managementv1.ListSessionVoiceChannelsRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no token = %v, want CodeFailedPrecondition", err)
	}
}

// TestListSessionVoiceChannelsSuccess: the guild's channels come back in lister
// order with the stored Default Voice Channel id for the picker's pre-select.
func TestListSessionVoiceChannelsSuccess(t *testing.T) {
	t.Parallel()
	client := newChannelsClient(t,
		&fakeChannelLister{channels: []discordshare.Channel{{ID: "111", Name: "Tavern"}, {ID: "222", Name: "War Room"}}},
		&fakeDeploymentReader{dep: storage.DeploymentConfig{GuildID: "g1", VoiceChannelID: "222"}})
	res, err := client.ListSessionVoiceChannels(context.Background(), connect.NewRequest(&managementv1.ListSessionVoiceChannelsRequest{}))
	if err != nil {
		t.Fatalf("ListSessionVoiceChannels: %v", err)
	}
	if got := len(res.Msg.Channels); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}
	if res.Msg.Channels[0].Id != "111" || res.Msg.Channels[0].Name != "Tavern" {
		t.Fatalf("first channel = %+v", res.Msg.Channels[0])
	}
	if res.Msg.DefaultChannelId != "222" {
		t.Fatalf("default_channel_id = %q, want 222", res.Msg.DefaultChannelId)
	}
}

// TestStartSessionPassesVoiceChannel: the request's voice_channel_id reaches
// Manager.Start verbatim, and an omitted one arrives as "" (the guild-default
// fallback contract).
func TestStartSessionPassesVoiceChannel(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		channel string
	}{
		"explicit pick": {channel: "123456789"},
		"guild default": {channel: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mgr := &fakeSessionManager{}
			client := newSessionClient(t, mgr, activeStore())
			if _, err := client.StartSession(context.Background(),
				connect.NewRequest(&managementv1.StartSessionRequest{VoiceChannelId: tc.channel})); err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			if mgr.startVoiceChan != tc.channel {
				t.Fatalf("manager got voice channel %q, want %q", mgr.startVoiceChan, tc.channel)
			}
		})
	}
}

// TestStartSessionRejectsNonSnowflakeChannel: a voice_channel_id that is not
// snowflake-shaped (a name, a URL) fails fast as InvalidArgument before any
// manager work.
func TestStartSessionRejectsNonSnowflakeChannel(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{}
	client := newSessionClient(t, mgr, activeStore())
	_, err := client.StartSession(context.Background(),
		connect.NewRequest(&managementv1.StartSessionRequest{VoiceChannelId: "general-voice"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("non-snowflake channel = %v, want CodeInvalidArgument", err)
	}
	if mgr.startCalls != 0 {
		t.Fatalf("manager Start ran %d times, want 0", mgr.startCalls)
	}
}
