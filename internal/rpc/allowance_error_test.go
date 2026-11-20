package rpc_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// requireAllowanceRefusal asserts err is THE plan-allowance refusal as a
// client receives it over the wire (#658): ResourceExhausted, the
// machine-readable reason the web keys its translated sentence on, and a
// message that is a sentence a GM can read as-is — no raw sentinel, no
// "internal error".
func requireAllowanceRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want the allowance refusal")
	}
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Fatalf("code = %v (%v), want ResourceExhausted", got, err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a connect error: %v", err)
	}
	if got := ce.Meta().Get("Glyphoxa-Error-Reason"); got != "allowance_exhausted" {
		t.Errorf("error reason metadata = %q, want allowance_exhausted (the web matches on it)", got)
	}
	msg := ce.Message()
	if !strings.Contains(msg, "included usage") || strings.Contains(msg, "billing:") || strings.Contains(msg, "internal error") {
		t.Errorf("message is not the user-facing sentence: %q", msg)
	}
}

// errWrappedAllowance is the sentinel as the engines return it: sometimes bare, sometimes
// wrapped on the way up. The mappers must read through the wrapping.
var errWrappedAllowance = fmt.Errorf("assist: %w", billing.ErrAllowanceExhausted)

func TestAllowanceRefusal_GeneratePersona(t *testing.T) {
	t.Parallel()
	client, req := personaErrClient(t, errWrappedAllowance)
	_, err := client.GeneratePersona(context.Background(), req)
	requireAllowanceRefusal(t, err)
}

func TestAllowanceRefusal_GenerateKnowledge(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	_, err := assistClient(t, store, &fakeAssistEngine{draftErr: billing.ErrAllowanceExhausted}).GenerateKnowledge(context.Background(),
		connect.NewRequest(&managementv1.GenerateKnowledgeRequest{Prompt: "the thieves' guild"}))
	requireAllowanceRefusal(t, err)
}

func TestAllowanceRefusal_GenerateMapImage(t *testing.T) {
	t.Parallel()
	client := genClient(t, newFakeMapStore(), &fakeMapGen{err: billing.ErrAllowanceExhausted})
	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a fishing town"}))
	requireAllowanceRefusal(t, err)
}

func TestAllowanceRefusal_SuggestMapPins(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	store.maps[mapID] = storage.CampaignMap{ID: mapID, Name: "Saltmarsh", AnchorNodeID: uuid.NullUUID{UUID: uuid.New(), Valid: true}}
	store.unpinned = []storage.KGNode{{ID: uuid.New(), Name: "The Snapping Line", Type: storage.KGNodeLocation}}
	client := genClient(t, store, &fakeMapGen{pickErr: errWrappedAllowance})
	_, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	requireAllowanceRefusal(t, err)
}

func TestAllowanceRefusal_GenerateNodePortrait(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	client := portraitClient(t, &fakePortraitStore{node: node}, newMemBlobs(), &fakePortraitGen{err: billing.ErrAllowanceExhausted})
	_, err := client.GenerateNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.GenerateNodePortraitRequest{NodeId: node.ID.String()}))
	requireAllowanceRefusal(t, err)
}

// GenerateRecap used to answer every engine failure with "internal error".
func TestAllowanceRefusal_GenerateRecap(t *testing.T) {
	t.Parallel()
	campaignID := uuid.New()
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID, Status: storage.VoiceSessionEnded}
	client := newRecapClient(t, &fakeSessionManager{}, recapStore(campaignID, vs), &fakeRecapEngine{err: billing.ErrAllowanceExhausted})
	_, err := client.GenerateRecap(context.Background(),
		connect.NewRequest(&managementv1.GenerateRecapRequest{SessionIds: []string{vs.ID.String()}}))
	requireAllowanceRefusal(t, err)
}

// The planning chat is a server stream: the reason must survive in the
// end-of-stream metadata too.
func TestAllowanceRefusal_PlanningExchange(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()
	client := newChatClient(t, store, &fakeChatEngine{err: billing.ErrAllowanceExhausted})
	created, err := client.CreatePlanningThread(context.Background(),
		connect.NewRequest(&managementv1.CreatePlanningThreadRequest{CampaignId: c.ID.String()}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stream, err := client.PlanningExchange(context.Background(), connect.NewRequest(&managementv1.PlanningExchangeRequest{
		CampaignId: c.ID.String(), ThreadId: created.Msg.GetThread().GetId(), Message: "hi",
	}))
	if err != nil {
		t.Fatalf("PlanningExchange: %v", err)
	}
	_, err = receiveAll(t, stream)
	requireAllowanceRefusal(t, err)
}

// soundClientWith is newSoundClient with the off-session metering seam wired.
func soundClientWith(t *testing.T, tenantID uuid.UUID, hstore rpc.HighlightStore, sstore *fakeSessionStore, enqueue rpc.HighlightEnqueuer, metering *billing.OffSession) managementv1connect.SessionServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithTenant(ctx, tenantID), req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, sstore, nil, nil)
	srv.SetHighlights(hstore, &fakeRPCBlobs{}, enqueue)
	srv.SetSoundEnrichment(configuredSoundFactory)
	srv.SetOffSessionMetering(metering)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
}

// SetHighlightSound only SCHEDULES the paid generation, and the job that runs
// it has no one to refuse: an exhausted allowance must be reported at the
// click, before the row changes and before anything is enqueued. A BYOK-plan
// Tenant under the same gate, and removing a sound (which spends nothing),
// both go through.
func TestSetHighlightSound_AllowanceGate(t *testing.T) {
	t.Parallel()
	tenantID, campaignID := uuid.New(), uuid.New()

	t.Run("exhausted is refused before any change", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		enq := &fakeHighlightEnqueuer{}
		client := soundClientWith(t, tenantID, store, campaignSessionStore(campaignID), enq, billingtest.Exhausted(&billingtest.Usage{}))

		_, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting}))
		requireAllowanceRefusal(t, err)
		if got, _ := store.GetHighlight(context.Background(), tenantID, h.ID); got.SoundKind != "" {
			t.Errorf("row mutated to kind %q despite the refusal", got.SoundKind)
		}
		if enq.calls != 0 {
			t.Errorf("enqueue calls = %d, want 0", enq.calls)
		}
	})

	t.Run("removing a sound is never gated", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		client := soundClientWith(t, tenantID, store, campaignSessionStore(campaignID), &fakeHighlightEnqueuer{}, billingtest.Exhausted(&billingtest.Usage{}))
		if _, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: ""})); err != nil {
			t.Fatalf("clearing a sound on an exhausted allowance: %v", err)
		}
	})

	t.Run("BYOK plan is not gated", func(t *testing.T) {
		store := newFakeHighlightStore(tenantID)
		h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightPromoted)
		enq := &fakeHighlightEnqueuer{}
		client := soundClientWith(t, tenantID, store, campaignSessionStore(campaignID), enq, billingtest.BYOK(&billingtest.Usage{}))
		if _, err := client.SetHighlightSound(context.Background(),
			connect.NewRequest(&managementv1.SetHighlightSoundRequest{Id: h.ID.String(), Kind: storage.SoundKindSting})); err != nil {
			t.Fatalf("BYOK tenant refused: %v", err)
		}
		if enq.calls != 1 {
			t.Errorf("enqueue calls = %d, want 1", enq.calls)
		}
	})
}
