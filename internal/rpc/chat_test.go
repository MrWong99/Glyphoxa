package rpc_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/planchat"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeChatStore is an in-memory chat store for one tenant.
type fakeChatStore struct {
	tenantID  uuid.UUID
	campaigns map[uuid.UUID]storage.Campaign
	threads   map[uuid.UUID]storage.PlanningThread
	messages  map[uuid.UUID][]storage.PlanningMessage
}

func newFakeChatStore(tenantID uuid.UUID) *fakeChatStore {
	return &fakeChatStore{
		tenantID:  tenantID,
		campaigns: map[uuid.UUID]storage.Campaign{},
		threads:   map[uuid.UUID]storage.PlanningThread{},
		messages:  map[uuid.UUID][]storage.PlanningMessage{},
	}
}

func (f *fakeChatStore) addCampaign() storage.Campaign {
	c := storage.Campaign{ID: uuid.New(), TenantID: f.tenantID, Name: "Test Campaign"}
	f.campaigns[c.ID] = c
	return c
}

func (f *fakeChatStore) GetCampaignInTenant(_ context.Context, tenantID, id uuid.UUID) (storage.Campaign, error) {
	c, ok := f.campaigns[id]
	if !ok || c.TenantID != tenantID {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
}

func (f *fakeChatStore) ListPlanningThreads(_ context.Context, campaignID uuid.UUID) ([]storage.PlanningThread, error) {
	var out []storage.PlanningThread
	for _, t := range f.threads {
		if t.CampaignID == campaignID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeChatStore) GetPlanningThread(_ context.Context, campaignID, id uuid.UUID) (storage.PlanningThread, error) {
	t, ok := f.threads[id]
	if !ok || t.CampaignID != campaignID {
		return storage.PlanningThread{}, storage.ErrNotFound
	}
	return t, nil
}

func (f *fakeChatStore) ListPlanningMessages(_ context.Context, campaignID, threadID uuid.UUID) ([]storage.PlanningMessage, error) {
	t, ok := f.threads[threadID]
	if !ok || t.CampaignID != campaignID {
		return nil, nil
	}
	return f.messages[threadID], nil
}

func (f *fakeChatStore) CreatePlanningThread(_ context.Context, campaignID uuid.UUID, title string) (storage.PlanningThread, error) {
	t := storage.PlanningThread{ID: uuid.New(), CampaignID: campaignID, Title: title}
	f.threads[t.ID] = t
	return t, nil
}

// The creator-scoped twins (ADR-0065) mirror the store: a thread whose
// CreatedBy is not the given user reads as absent.

func (f *fakeChatStore) ownedBy(campaignID, id, createdBy uuid.UUID) (storage.PlanningThread, bool) {
	t, ok := f.threads[id]
	if !ok || t.CampaignID != campaignID || !t.CreatedBy.Valid || t.CreatedBy.UUID != createdBy {
		return storage.PlanningThread{}, false
	}
	return t, true
}

func (f *fakeChatStore) CreatePlanningThreadBy(_ context.Context, campaignID uuid.UUID, title string, createdBy uuid.UUID) (storage.PlanningThread, error) {
	t := storage.PlanningThread{ID: uuid.New(), CampaignID: campaignID, Title: title,
		CreatedBy: uuid.NullUUID{UUID: createdBy, Valid: true}}
	f.threads[t.ID] = t
	return t, nil
}

func (f *fakeChatStore) GetPlanningThreadBy(_ context.Context, campaignID, id, createdBy uuid.UUID) (storage.PlanningThread, error) {
	t, ok := f.ownedBy(campaignID, id, createdBy)
	if !ok {
		return storage.PlanningThread{}, storage.ErrNotFound
	}
	return t, nil
}

func (f *fakeChatStore) ListPlanningThreadsBy(_ context.Context, campaignID, createdBy uuid.UUID) ([]storage.PlanningThread, error) {
	var out []storage.PlanningThread
	for id := range f.threads {
		if t, ok := f.ownedBy(campaignID, id, createdBy); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeChatStore) RenamePlanningThreadBy(_ context.Context, campaignID, id, createdBy uuid.UUID, title string) (storage.PlanningThread, error) {
	t, ok := f.ownedBy(campaignID, id, createdBy)
	if !ok {
		return storage.PlanningThread{}, storage.ErrNotFound
	}
	t.Title = title
	f.threads[id] = t
	return t, nil
}

func (f *fakeChatStore) DeletePlanningThreadBy(_ context.Context, campaignID, id, createdBy uuid.UUID) error {
	if _, ok := f.ownedBy(campaignID, id, createdBy); !ok {
		return storage.ErrNotFound
	}
	delete(f.threads, id)
	delete(f.messages, id)
	return nil
}

func (f *fakeChatStore) RenamePlanningThread(_ context.Context, campaignID, id uuid.UUID, title string) (storage.PlanningThread, error) {
	t, ok := f.threads[id]
	if !ok || t.CampaignID != campaignID {
		return storage.PlanningThread{}, storage.ErrNotFound
	}
	t.Title = title
	f.threads[id] = t
	return t, nil
}

func (f *fakeChatStore) DeletePlanningThread(_ context.Context, campaignID, id uuid.UUID) error {
	t, ok := f.threads[id]
	if !ok || t.CampaignID != campaignID {
		return storage.ErrNotFound
	}
	delete(f.threads, id)
	delete(f.messages, id)
	return nil
}

// fakeChatEngine scripts one exchange: streamed deltas/tools, then result or
// error.
type fakeChatEngine struct {
	deltas []string
	tools  []string
	err    error

	gotText     string
	gotCampaign uuid.UUID
}

func (f *fakeChatEngine) Exchange(_ context.Context, _ uuid.UUID, campaign storage.Campaign, threadID uuid.UUID, text string, ev planchat.Events) (planchat.Result, error) {
	f.gotText = text
	f.gotCampaign = campaign.ID
	for _, d := range f.deltas {
		if err := ev.Text(d); err != nil {
			return planchat.Result{}, err
		}
	}
	for _, name := range f.tools {
		ev.Tool(name, false)
	}
	if f.err != nil {
		return planchat.Result{}, f.err
	}
	asst := storage.PlanningMessage{ID: uuid.New(), ThreadID: threadID, Role: storage.PlanningRoleAssistant, Content: strings.Join(f.deltas, "")}
	return planchat.Result{
		AssistantMessage: asst,
		Thread:           storage.PlanningThread{ID: threadID, CampaignID: campaign.ID, Title: "titled"},
	}, nil
}

// tenantInjector injects the tenant on BOTH call shapes — the chat service is
// the first with a streaming procedure.
type tenantInjector struct{ tenantID uuid.UUID }

func (i tenantInjector) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(auth.WithTenant(ctx, i.tenantID), req)
	}
}

func (i tenantInjector) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i tenantInjector) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(auth.WithTenant(ctx, i.tenantID), conn)
	}
}

func newChatClient(t *testing.T, store *fakeChatStore, engine rpc.ChatEngine) managementv1connect.ChatServiceClient {
	t.Helper()
	server := rpc.NewChatServer(store, nil)
	if engine != nil {
		server.SetEngine(engine)
	}
	mux := http.NewServeMux()
	mux.Handle(server.Handler(connect.WithInterceptors(tenantInjector{tenantID: store.tenantID})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewChatServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

func TestChatThreadCRUD(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()
	client := newChatClient(t, store, nil)
	ctx := context.Background()

	created, err := client.CreatePlanningThread(ctx, connect.NewRequest(&managementv1.CreatePlanningThreadRequest{
		CampaignId: c.ID.String(), Title: "Session 12 prep",
	}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	threadID := created.Msg.GetThread().GetId()

	listed, err := client.ListPlanningThreads(ctx, connect.NewRequest(&managementv1.ListPlanningThreadsRequest{CampaignId: c.ID.String()}))
	if err != nil || len(listed.Msg.GetThreads()) != 1 {
		t.Fatalf("List: %v (%d threads)", err, len(listed.Msg.GetThreads()))
	}

	renamed, err := client.RenamePlanningThread(ctx, connect.NewRequest(&managementv1.RenamePlanningThreadRequest{
		CampaignId: c.ID.String(), ThreadId: threadID, Title: "Renamed",
	}))
	if err != nil || renamed.Msg.GetThread().GetTitle() != "Renamed" {
		t.Fatalf("Rename: %v (%q)", err, renamed.Msg.GetThread().GetTitle())
	}

	got, err := client.GetPlanningThread(ctx, connect.NewRequest(&managementv1.GetPlanningThreadRequest{
		CampaignId: c.ID.String(), ThreadId: threadID,
	}))
	if err != nil || got.Msg.GetThread().GetTitle() != "Renamed" {
		t.Fatalf("Get: %v", err)
	}

	if _, err := client.DeletePlanningThread(ctx, connect.NewRequest(&managementv1.DeletePlanningThreadRequest{
		CampaignId: c.ID.String(), ThreadId: threadID,
	})); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := client.GetPlanningThread(ctx, connect.NewRequest(&managementv1.GetPlanningThreadRequest{
		CampaignId: c.ID.String(), ThreadId: threadID,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get after delete = %v, want NotFound", err)
	}
}

func TestChatCampaignScoping(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	client := newChatClient(t, store, nil)

	// A campaign in ANOTHER tenant is invisible — NotFound, never a permission
	// hint.
	foreign := storage.Campaign{ID: uuid.New(), TenantID: uuid.New()}
	store.campaigns[foreign.ID] = foreign
	_, err := client.ListPlanningThreads(context.Background(), connect.NewRequest(&managementv1.ListPlanningThreadsRequest{
		CampaignId: foreign.ID.String(),
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign campaign = %v, want NotFound", err)
	}

	// Garbage ids are InvalidArgument.
	_, err = client.ListPlanningThreads(context.Background(), connect.NewRequest(&managementv1.ListPlanningThreadsRequest{
		CampaignId: "not-a-uuid",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad campaign id = %v, want InvalidArgument", err)
	}
}

func TestChatRenameValidation(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()
	client := newChatClient(t, store, nil)
	created, err := client.CreatePlanningThread(context.Background(), connect.NewRequest(&managementv1.CreatePlanningThreadRequest{
		CampaignId: c.ID.String(),
	}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for name, title := range map[string]string{
		"empty":    "   ",
		"too long": strings.Repeat("x", 300),
	} {
		_, err := client.RenamePlanningThread(context.Background(), connect.NewRequest(&managementv1.RenamePlanningThreadRequest{
			CampaignId: c.ID.String(), ThreadId: created.Msg.GetThread().GetId(), Title: title,
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s title = %v, want InvalidArgument", name, err)
		}
	}
}

// receiveAll drains a PlanningExchange stream into its frames.
func receiveAll(t *testing.T, stream *connect.ServerStreamForClient[managementv1.PlanningExchangeResponse]) ([]*managementv1.PlanningExchangeResponse, error) {
	t.Helper()
	var frames []*managementv1.PlanningExchangeResponse
	for stream.Receive() {
		frames = append(frames, stream.Msg())
	}
	return frames, stream.Err()
}

func TestPlanningExchange_StreamsFrames(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()
	engine := &fakeChatEngine{deltas: []string{"Hello ", "GM."}, tools: []string{"search_transcripts"}}
	client := newChatClient(t, store, engine)
	ctx := context.Background()

	created, err := client.CreatePlanningThread(ctx, connect.NewRequest(&managementv1.CreatePlanningThreadRequest{CampaignId: c.ID.String()}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stream, err := client.PlanningExchange(ctx, connect.NewRequest(&managementv1.PlanningExchangeRequest{
		CampaignId: c.ID.String(), ThreadId: created.Msg.GetThread().GetId(), Message: "What happened with the cult?",
	}))
	if err != nil {
		t.Fatalf("PlanningExchange: %v", err)
	}
	frames, err := receiveAll(t, stream)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var text strings.Builder
	var tools []string
	var done *managementv1.PlanningExchangeDone
	for _, f := range frames {
		switch fr := f.GetFrame().(type) {
		case *managementv1.PlanningExchangeResponse_Text:
			text.WriteString(fr.Text.GetDelta())
		case *managementv1.PlanningExchangeResponse_Tool:
			tools = append(tools, fr.Tool.GetToolName())
		case *managementv1.PlanningExchangeResponse_Done:
			done = fr.Done
		}
	}
	if text.String() != "Hello GM." {
		t.Errorf("streamed text = %q", text.String())
	}
	if len(tools) != 1 || tools[0] != "search_transcripts" {
		t.Errorf("tool frames = %v", tools)
	}
	if done == nil || done.GetAssistantMessage().GetContent() != "Hello GM." {
		t.Errorf("done frame = %+v", done)
	}
	if engine.gotText != "What happened with the cult?" || engine.gotCampaign != c.ID {
		t.Errorf("engine saw text=%q campaign=%s", engine.gotText, engine.gotCampaign)
	}
}

func TestPlanningExchange_ErrorVocabulary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		want   connect.Code
		reason string
	}{
		{"allowance", planchat.ErrAllowanceExhausted, connect.CodeResourceExhausted, "allowance_exhausted"},
		{"entitlement", llmbuild.ErrNoPlatformKeyEntitlement, connect.CodeFailedPrecondition, ""},
		{"thread gone", storage.ErrNotFound, connect.CodeNotFound, ""},
		// Not a provider rejection the web can point at Setup for.
		{"anything else", errors.New("provider exploded"), connect.CodeUnavailable, "unclassified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeChatStore(uuid.New())
			c := store.addCampaign()
			client := newChatClient(t, store, &fakeChatEngine{err: tc.err})
			created, err := client.CreatePlanningThread(context.Background(), connect.NewRequest(&managementv1.CreatePlanningThreadRequest{CampaignId: c.ID.String()}))
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
			if connect.CodeOf(err) != tc.want {
				t.Errorf("code = %v (%v), want %v", connect.CodeOf(err), err, tc.want)
			}
			var ce *connect.Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want a *connect.Error", err)
			}
			if got := ce.Meta().Get("Glyphoxa-Error-Reason"); got != tc.reason {
				t.Errorf("Glyphoxa-Error-Reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// stoppingChatEngine streams one delta, then waits for the client to go away
// and — like the real engine — keeps sending into the dead stream before it
// persists the partial reply and reports ErrStopped.
type stoppingChatEngine struct {
	finished    chan struct{}
	lateSendErr error
}

func (f *stoppingChatEngine) Exchange(ctx context.Context, _ uuid.UUID, _ storage.Campaign, threadID uuid.UUID, _ string, ev planchat.Events) (planchat.Result, error) {
	defer close(f.finished)
	if err := ev.Text("The docks are"); err != nil {
		return planchat.Result{}, err
	}
	<-ctx.Done()
	f.lateSendErr = ev.Text(" guarded.")
	ev.Tool("find_node", false) // advisory: must not panic on a dead stream
	asst := storage.PlanningMessage{ID: uuid.New(), ThreadID: threadID, Role: storage.PlanningRoleAssistant, Content: "The docks are"}
	return planchat.Result{AssistantMessage: asst}, planchat.ErrStopped
}

// levelRecorder is a slog handler that remembers the records it saw.
type levelRecorder struct {
	mu       sync.Mutex
	levels   []slog.Level
	messages []string
}

func (h *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *levelRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levels = append(h.levels, r.Level)
	h.messages = append(h.messages, r.Message)
	return nil
}

func (h *levelRecorder) saw(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.messages, msg)
}
func (h *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelRecorder) WithGroup(string) slog.Handler      { return h }

func (h *levelRecorder) errors() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, l := range h.levels {
		if l >= slog.LevelError {
			n++
		}
	}
	return n
}

// TestPlanningExchange_StopIsNotAFault: the GM's Stop cancels the request. The
// handler lets the engine run on to persist the partial (its late sends fail
// quietly) and records the stop as information, not as an exchange failure.
func TestPlanningExchange_StopIsNotAFault(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()
	thread, _ := store.CreatePlanningThread(context.Background(), c.ID, "")
	engine := &stoppingChatEngine{finished: make(chan struct{})}
	logs := &levelRecorder{}

	server := rpc.NewChatServer(store, slog.New(logs))
	server.SetEngine(engine)
	mux := http.NewServeMux()
	mux.Handle(server.Handler(connect.WithInterceptors(tenantInjector{tenantID: store.tenantID})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewChatServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.PlanningExchange(ctx, connect.NewRequest(&managementv1.PlanningExchangeRequest{
		CampaignId: c.ID.String(), ThreadId: thread.ID.String(), Message: "Who guards the docks?",
	}))
	if err != nil {
		t.Fatalf("PlanningExchange: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("no first frame: %v", stream.Err())
	}
	cancel() // Stop

	select {
	case <-engine.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine never finished after the client stopped")
	}
	if engine.lateSendErr == nil {
		t.Error("a send into the stopped stream reported success")
	}
	// The handler's own log line lands after the engine returns.
	deadline := time.Now().Add(5 * time.Second)
	for !logs.saw("PlanningExchange: stopped by the client") {
		if time.Now().After(deadline) {
			t.Fatal("the handler never recorded the stop")
		}
		runtime.Gosched()
	}
	if n := logs.errors(); n != 0 {
		t.Errorf("a Stop logged %d error records, want none", n)
	}
}

func TestPlanningExchange_Guards(t *testing.T) {
	t.Parallel()
	store := newFakeChatStore(uuid.New())
	c := store.addCampaign()

	// Unwired engine → Unimplemented (the SetSearch posture).
	bare := newChatClient(t, store, nil)
	stream, err := bare.PlanningExchange(context.Background(), connect.NewRequest(&managementv1.PlanningExchangeRequest{
		CampaignId: c.ID.String(), ThreadId: uuid.NewString(), Message: "hi",
	}))
	if err == nil {
		_, err = receiveAll(t, stream)
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("unwired engine = %v, want Unimplemented", err)
	}

	// Empty and oversized messages are InvalidArgument before the engine runs.
	client := newChatClient(t, store, &fakeChatEngine{deltas: []string{"x"}})
	for name, msg := range map[string]string{
		"empty":    "  ",
		"too long": strings.Repeat("x", planchat.MaxMessageRunes+1),
	} {
		stream, err := client.PlanningExchange(context.Background(), connect.NewRequest(&managementv1.PlanningExchangeRequest{
			CampaignId: c.ID.String(), ThreadId: uuid.NewString(), Message: msg,
		}))
		if err == nil {
			_, err = receiveAll(t, stream)
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s message = %v, want InvalidArgument", name, err)
		}
	}
}
