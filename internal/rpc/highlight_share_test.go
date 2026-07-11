package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// --- fakes for the sharing seam (#310) ------------------------------------------

var errPersist = errors.New("persist boom")

type postCall struct {
	channelID, caption, filename, contentType string
	data                                      []byte
}

type fakeSharer struct {
	mu        sync.Mutex
	channels  []discordshare.Channel
	listErr   error
	postErr   error
	postCalls []postCall
}

func (f *fakeSharer) ListTextChannels(context.Context) ([]discordshare.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channels, f.listErr
}

func (f *fakeSharer) PostClip(_ context.Context, channelID, caption, filename, contentType string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return f.postErr
	}
	f.postCalls = append(f.postCalls, postCall{channelID, caption, filename, contentType, append([]byte(nil), data...)})
	return nil
}

func (f *fakeSharer) calls() []postCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postCall(nil), f.postCalls...)
}

type fakeReplayer struct {
	mu       sync.Mutex
	clipKeys []string
	err      error
}

func (f *fakeReplayer) ReplayHighlight(_ context.Context, clipKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.clipKeys = append(f.clipKeys, clipKey)
	return nil
}

type fakeShareStore struct {
	mu     sync.Mutex
	chans  map[uuid.UUID]string
	setErr error
}

func newFakeShareStore() *fakeShareStore { return &fakeShareStore{chans: map[uuid.UUID]string{}} }

func (f *fakeShareStore) GetCampaignShareChannel(_ context.Context, id uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.chans[id]
	if !ok {
		return "", storage.ErrNotFound
	}
	return ch, nil
}

func (f *fakeShareStore) SetCampaignShareChannel(_ context.Context, id uuid.UUID, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.chans[id] = ch
	return nil
}

func (f *fakeShareStore) get(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chans[id]
}

// newShareClient mounts a SessionServer with BOTH the highlight seam and the sharing
// seam wired, an authenticated operator on tenantID, and the Active Campaign resolved
// from sstore.
func newShareClient(t *testing.T, tenantID uuid.UUID, hstore rpc.HighlightStore, blobs *fakeRPCBlobs, sstore *fakeSessionStore, sharer rpc.HighlightSharer, replayer rpc.HighlightReplayer, shareStore rpc.ShareChannelStore) managementv1connect.SessionServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithTenant(ctx, tenantID), req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, sstore, nil, nil)
	srv.SetHighlights(hstore, blobs, nil)
	srv.SetSharing(sharer, replayer, shareStore)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
}

func shareToChannelReq(id, channelID string) *managementv1.ShareHighlightRequest {
	return &managementv1.ShareHighlightRequest{Id: id, Mode: &managementv1.ShareHighlightRequest_TextChannelId{TextChannelId: channelID}}
}

func shareReplayReq(id string) *managementv1.ShareHighlightRequest {
	return &managementv1.ShareHighlightRequest{Id: id, Mode: &managementv1.ShareHighlightRequest_VoiceReplay{VoiceReplay: true}}
}

// TestShareHighlight_NotFoundPosture pins the errNoSuchHighlight posture: an unknown,
// foreign-tenant, or cross-campaign id is CodeNotFound and never calls the sharer.
func TestShareHighlight_NotFoundPosture(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	foreign := seedRPCHighlight(store, uuid.New(), uuid.New(), campaignID, storage.HighlightPromoted)
	crossCampaign := seedRPCHighlight(store, tenantID, uuid.New(), uuid.New(), storage.HighlightPromoted)
	sharer := &fakeSharer{}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, newFakeShareStore())

	for name, id := range map[string]string{
		"unknown":        uuid.New().String(),
		"unparsable":     "not-a-uuid",
		"foreign-tenant": foreign.ID.String(),
		"cross-campaign": crossCampaign.ID.String(),
	} {
		_, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(id, "chan1")))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: want CodeNotFound, got %v", name, err)
		}
	}
	if len(sharer.calls()) != 0 {
		t.Fatalf("sharer called on a NotFound id: %v", sharer.calls())
	}
}

// TestShareHighlight_CandidateRefused pins the promoted-only rule (#310, ADR-0051): a
// candidate Highlight is CodeFailedPrecondition and never leaves the instance.
func TestShareHighlight_CandidateRefused(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightCandidate)
	sharer := &fakeSharer{}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, newFakeShareStore())
	_, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(h.ID.String(), "chan1")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("candidate: want CodeFailedPrecondition, got %v", err)
	}
	if len(sharer.calls()) != 0 {
		t.Fatalf("sharer called for a candidate: %v", sharer.calls())
	}
}

// TestShareHighlight_OversizeRefusedBeforeFetch pins the refuse-no-reencode rule: an
// oversize clip is CodeFailedPrecondition BEFORE any blob fetch (the sharer + blob
// seam are never touched).
func TestShareHighlight_OversizeRefusedBeforeFetch(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	h.ClipSizeBytes = discordshare.MaxUploadBytes + 1
	store.put(h)
	sharer := &fakeSharer{}
	blobs := &fakeRPCBlobs{}

	client := newShareClient(t, tenantID, store, blobs, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, newFakeShareStore())
	_, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(h.ID.String(), "chan1")))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("oversize: want CodeFailedPrecondition, got %v", err)
	}
	if len(sharer.calls()) != 0 {
		t.Fatalf("sharer called for an oversize clip: %v", sharer.calls())
	}
	if len(blobs.gotKeys) != 0 {
		t.Fatalf("blob fetched for an oversize clip: %v", blobs.gotKeys)
	}
}

// TestShareHighlight_PostHappy pins the file-share success path: the clip bytes +
// caption reach the sharer with the right filename/type, and the channel is
// remembered per campaign.
func TestShareHighlight_PostHappy(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	sharer := &fakeSharer{}
	blobs := &fakeRPCBlobs{data: []byte("WAVBYTES")}
	shareStore := newFakeShareStore()

	client := newShareClient(t, tenantID, store, blobs, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, shareStore)
	if _, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(h.ID.String(), "chanX"))); err != nil {
		t.Fatalf("share: %v", err)
	}
	calls := sharer.calls()
	if len(calls) != 1 {
		t.Fatalf("sharer calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.channelID != "chanX" {
		t.Errorf("channelID = %q, want chanX", c.channelID)
	}
	if c.caption != "nat 20" {
		t.Errorf("caption = %q, want the excerpt", c.caption)
	}
	if c.filename != "highlight.wav" {
		t.Errorf("filename = %q, want highlight.wav", c.filename)
	}
	if c.contentType != "audio/wav" {
		t.Errorf("contentType = %q, want audio/wav", c.contentType)
	}
	if string(c.data) != "WAVBYTES" {
		t.Errorf("data = %q, want the clip bytes", c.data)
	}
	if shareStore.get(campaignID) != "chanX" {
		t.Errorf("last share channel = %q, want chanX", shareStore.get(campaignID))
	}
}

// TestShareHighlight_PostPersistFailureStillSucceeds pins that a failure to remember
// the channel does NOT fail the share (the post already landed).
func TestShareHighlight_PostPersistFailureStillSucceeds(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	sharer := &fakeSharer{}
	shareStore := newFakeShareStore()
	shareStore.setErr = errPersist

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, shareStore)
	if _, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(h.ID.String(), "chanX"))); err != nil {
		t.Fatalf("share must succeed despite persist failure, got %v", err)
	}
	if len(sharer.calls()) != 1 {
		t.Fatalf("sharer calls = %d, want 1", len(sharer.calls()))
	}
}

// TestShareHighlight_DiscordErrorIsUnavailable pins that a Discord API failure maps to
// CodeUnavailable (readable), not a raw internal error.
func TestShareHighlight_DiscordErrorIsUnavailable(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	sharer := &fakeSharer{postErr: &discordshare.APIError{Op: "post file", Status: 413}}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, newFakeShareStore())
	_, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareToChannelReq(h.ID.String(), "chanX")))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Discord API error: want CodeUnavailable, got %v", err)
	}
}

// TestShareHighlight_VoiceReplayHappy pins that voice_replay hands the clip key to the
// replayer.
func TestShareHighlight_VoiceReplayHappy(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	replayer := &fakeReplayer{}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeSharer{}, replayer, newFakeShareStore())
	if _, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareReplayReq(h.ID.String()))); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayer.clipKeys) != 1 || replayer.clipKeys[0] != h.ClipKey {
		t.Fatalf("replayer clip keys = %v, want [%s]", replayer.clipKeys, h.ClipKey)
	}
}

// TestShareHighlight_VoiceReplayNoSession pins ErrNoActiveSession → FailedPrecondition.
func TestShareHighlight_VoiceReplayNoSession(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
	replayer := &fakeReplayer{err: session.ErrNoActiveSession}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), &fakeSharer{}, replayer, newFakeShareStore())
	_, err := client.ShareHighlight(context.Background(), connect.NewRequest(shareReplayReq(h.ID.String())))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no session: want CodeFailedPrecondition, got %v", err)
	}
}

// TestListShareChannels_ChannelsAndLast pins the dialog source: the guild's channels
// plus the campaign's remembered channel.
func TestListShareChannels_ChannelsAndLast(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	sharer := &fakeSharer{channels: []discordshare.Channel{{ID: "10", Name: "general"}, {ID: "20", Name: "highlights"}}}
	shareStore := newFakeShareStore()
	shareStore.chans[campaignID] = "20"

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, shareStore)
	res, err := client.ListShareChannels(context.Background(), connect.NewRequest(&managementv1.ListShareChannelsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Msg.GetChannels()) != 2 {
		t.Fatalf("channels = %d, want 2", len(res.Msg.GetChannels()))
	}
	if res.Msg.GetLastShareChannelId() != "20" {
		t.Errorf("last = %q, want 20", res.Msg.GetLastShareChannelId())
	}
}

// TestListShareChannels_NoTokenFailedPrecondition pins that a missing Bot token maps
// to CodeFailedPrecondition ("save a Discord Bot token first").
func TestListShareChannels_NoTokenFailedPrecondition(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	sharer := &fakeSharer{listErr: rpc.ErrNoDiscordToken}

	client := newShareClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID), sharer, &fakeReplayer{}, newFakeShareStore())
	_, err := client.ListShareChannels(context.Background(), connect.NewRequest(&managementv1.ListShareChannelsRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no token: want CodeFailedPrecondition, got %v", err)
	}
}
