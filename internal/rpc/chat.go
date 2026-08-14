package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/planchat"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ChatServer implements the Connect ChatService (#592, ADR-0062): the Butler
// planning chat's thread CRUD plus the streaming exchange. GM-only posture:
// the web tier has a single principal today — the operator (ADR-0039/0055
// interim), who IS the GM — so the auth stack is the gate, exactly as the
// SessionService search RPCs (#591) document. When tenant_members lands
// (ADR-0002), the gm Member Role check slots in here.
//
// Every campaign_id on the wire is resolved against the session's tenant; an
// id outside it is CodeNotFound, never a permission hint.
type ChatServer struct {
	store  chatStore
	engine ChatEngine // nil until SetEngine: exchange reports CodeUnimplemented
	log    *slog.Logger
}

// chatStore is the narrow store surface the handlers need; *storage.Store
// satisfies it.
type chatStore interface {
	GetCampaignInTenant(ctx context.Context, tenantID, id uuid.UUID) (storage.Campaign, error)
	ListPlanningThreads(ctx context.Context, campaignID uuid.UUID) ([]storage.PlanningThread, error)
	GetPlanningThread(ctx context.Context, campaignID, id uuid.UUID) (storage.PlanningThread, error)
	ListPlanningMessages(ctx context.Context, campaignID, threadID uuid.UUID) ([]storage.PlanningMessage, error)
	CreatePlanningThread(ctx context.Context, campaignID uuid.UUID, title string) (storage.PlanningThread, error)
	RenamePlanningThread(ctx context.Context, campaignID, id uuid.UUID, title string) (storage.PlanningThread, error)
	DeletePlanningThread(ctx context.Context, campaignID, id uuid.UUID) error
}

// ChatEngine is the exchange surface [planchat.Engine] provides; the seam a
// unit test fakes to drive the streaming handler without an LLM.
type ChatEngine interface {
	Exchange(ctx context.Context, tenantID uuid.UUID, campaign storage.Campaign, threadID uuid.UUID, text string, ev planchat.Events) (planchat.Result, error)
}

var _ managementv1connect.ChatServiceHandler = (*ChatServer)(nil)

// NewChatServer builds the ChatService over the store. The exchange engine is
// wired separately ([ChatServer.SetEngine]) so keyless test compositions can
// exercise the thread CRUD without an LLM stack.
func NewChatServer(store chatStore, log *slog.Logger) *ChatServer {
	if log == nil {
		log = slog.Default()
	}
	return &ChatServer{store: store, log: log}
}

// SetEngine wires the planning chat engine. Called once at boot; unwired,
// PlanningExchange reports CodeUnimplemented (the SetSearch posture).
func (s *ChatServer) SetEngine(engine ChatEngine) {
	s.engine = engine
}

// Handler builds the Connect HTTP handler for ChatService and returns its
// mount path + handler, mirroring (*CampaignServer).Handler.
func (s *ChatServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewChatServiceHandler(s, opts...)
}

// maxThreadTitleRunes bounds a thread title on the wire.
const maxThreadTitleRunes = 200

// chatCampaign resolves and authorizes the request's campaign: parsed, then
// loaded WITHIN the session's tenant — an id in another tenant reads back as
// CodeNotFound (the storage scoping makes it invisible, never forbidden).
func (s *ChatServer) chatCampaign(ctx context.Context, rawCampaignID string) (storage.Campaign, *connect.Error) {
	tenantID, ok := auth.TenantID(ctx)
	if !ok {
		return storage.Campaign{}, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}
	id, err := uuid.Parse(rawCampaignID)
	if err != nil {
		return storage.Campaign{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}
	c, err := s.store.GetCampaignInTenant(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.Campaign{}, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
	}
	if err != nil {
		s.log.Error("chat: load campaign failed", "campaign_id", id, "err", err)
		return storage.Campaign{}, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return c, nil
}

// parseThreadID validates a thread id on the wire.
func parseThreadID(raw string) (uuid.UUID, *connect.Error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid thread id"))
	}
	return id, nil
}

// ListPlanningThreads returns the campaign's threads, most recently touched
// first.
func (s *ChatServer) ListPlanningThreads(
	ctx context.Context,
	req *connect.Request[managementv1.ListPlanningThreadsRequest],
) (*connect.Response[managementv1.ListPlanningThreadsResponse], error) {
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return nil, cerr
	}
	threads, err := s.store.ListPlanningThreads(ctx, c.ID)
	if err != nil {
		s.log.Error("ListPlanningThreads: store list failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := mapSlice(threads, toProtoPlanningThread)
	return connect.NewResponse(&managementv1.ListPlanningThreadsResponse{Threads: out}), nil
}

// GetPlanningThread returns one thread with its messages in order.
func (s *ChatServer) GetPlanningThread(
	ctx context.Context,
	req *connect.Request[managementv1.GetPlanningThreadRequest],
) (*connect.Response[managementv1.GetPlanningThreadResponse], error) {
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return nil, cerr
	}
	threadID, terr := parseThreadID(req.Msg.GetThreadId())
	if terr != nil {
		return nil, terr
	}
	thread, err := s.store.GetPlanningThread(ctx, c.ID, threadID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		s.log.Error("GetPlanningThread: store get failed", "thread_id", threadID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	messages, err := s.store.ListPlanningMessages(ctx, c.ID, threadID)
	if err != nil {
		s.log.Error("GetPlanningThread: list messages failed", "thread_id", threadID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := mapSlice(messages, toProtoPlanningMessage)
	return connect.NewResponse(&managementv1.GetPlanningThreadResponse{
		Thread:   toProtoPlanningThread(thread),
		Messages: out,
	}), nil
}

// CreatePlanningThread creates an empty thread; title may be empty (the first
// exchange auto-fills it).
func (s *ChatServer) CreatePlanningThread(
	ctx context.Context,
	req *connect.Request[managementv1.CreatePlanningThreadRequest],
) (*connect.Response[managementv1.CreatePlanningThreadResponse], error) {
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return nil, cerr
	}
	title := strings.TrimSpace(req.Msg.GetTitle())
	if utf8.RuneCountInString(title) > maxThreadTitleRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title is too long"))
	}
	thread, err := s.store.CreatePlanningThread(ctx, c.ID, title)
	if err != nil {
		s.log.Error("CreatePlanningThread: store create failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreatePlanningThreadResponse{Thread: toProtoPlanningThread(thread)}), nil
}

// RenamePlanningThread sets a thread's title (required non-empty).
func (s *ChatServer) RenamePlanningThread(
	ctx context.Context,
	req *connect.Request[managementv1.RenamePlanningThreadRequest],
) (*connect.Response[managementv1.RenamePlanningThreadResponse], error) {
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return nil, cerr
	}
	threadID, terr := parseThreadID(req.Msg.GetThreadId())
	if terr != nil {
		return nil, terr
	}
	title := strings.TrimSpace(req.Msg.GetTitle())
	if title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title must not be empty"))
	}
	if utf8.RuneCountInString(title) > maxThreadTitleRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title is too long"))
	}
	thread, err := s.store.RenamePlanningThread(ctx, c.ID, threadID, title)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		s.log.Error("RenamePlanningThread: store rename failed", "thread_id", threadID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.RenamePlanningThreadResponse{Thread: toProtoPlanningThread(thread)}), nil
}

// DeletePlanningThread removes a thread and its messages.
func (s *ChatServer) DeletePlanningThread(
	ctx context.Context,
	req *connect.Request[managementv1.DeletePlanningThreadRequest],
) (*connect.Response[managementv1.DeletePlanningThreadResponse], error) {
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return nil, cerr
	}
	threadID, terr := parseThreadID(req.Msg.GetThreadId())
	if terr != nil {
		return nil, terr
	}
	err := s.store.DeletePlanningThread(ctx, c.ID, threadID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if err != nil {
		s.log.Error("DeletePlanningThread: store delete failed", "thread_id", threadID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.DeletePlanningThreadResponse{}), nil
}

// streamEvents adapts one exchange's engine events onto the server stream.
// Send errors (a gone client) propagate through Text and abort the exchange.
type streamEvents struct {
	stream *connect.ServerStream[managementv1.PlanningExchangeResponse]
}

func (s streamEvents) Text(delta string) error {
	if delta == "" {
		return nil
	}
	return s.stream.Send(&managementv1.PlanningExchangeResponse{
		Frame: &managementv1.PlanningExchangeResponse_Text{
			Text: &managementv1.PlanningTextDelta{Delta: delta},
		},
	})
}

func (s streamEvents) Tool(name string, isErr bool) {
	// A tool-activity frame is advisory UI garnish: a send failure here is
	// ignored — the next Text send surfaces the dead stream and aborts.
	_ = s.stream.Send(&managementv1.PlanningExchangeResponse{
		Frame: &managementv1.PlanningExchangeResponse_Tool{
			Tool: &managementv1.PlanningToolActivity{ToolName: name, IsError: isErr},
		},
	})
}

// PlanningExchange runs one chat exchange, streaming deltas and tool activity,
// closing with one done frame on success. See chatExchangeErr for the error
// vocabulary.
func (s *ChatServer) PlanningExchange(
	ctx context.Context,
	req *connect.Request[managementv1.PlanningExchangeRequest],
	stream *connect.ServerStream[managementv1.PlanningExchangeResponse],
) error {
	if s.engine == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("planning chat is not enabled on this server"))
	}
	c, cerr := s.chatCampaign(ctx, req.Msg.GetCampaignId())
	if cerr != nil {
		return cerr
	}
	threadID, terr := parseThreadID(req.Msg.GetThreadId())
	if terr != nil {
		return terr
	}
	text := strings.TrimSpace(req.Msg.GetMessage())
	if text == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("message must not be empty"))
	}
	if utf8.RuneCountInString(text) > planchat.MaxMessageRunes {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("message is too long"))
	}
	tenantID, _ := auth.TenantID(ctx) // present: chatCampaign already required it

	res, err := s.engine.Exchange(ctx, tenantID, c, threadID, text, streamEvents{stream: stream})
	if err != nil {
		return s.chatExchangeErr(threadID, err)
	}
	return stream.Send(&managementv1.PlanningExchangeResponse{
		Frame: &managementv1.PlanningExchangeResponse_Done{
			Done: &managementv1.PlanningExchangeDone{
				AssistantMessage: toProtoPlanningMessage(res.AssistantMessage),
				Thread:           toProtoPlanningThread(res.Thread),
			},
		},
	})
}

// chatExchangeSubject names what the GM asked this surface for in upstream
// rejections (the assistSubject discipline).
const chatExchangeSubject = "the planning chat"

// chatExchangeErr maps an exchange failure onto its Connect code:
//
//   - an exhausted ADR-0055 allowance is CodeResourceExhausted — the clear
//     in-chat refusal ADR-0062 requires, actionable (upgrade / next month);
//   - a refused platform-key entitlement is CodeFailedPrecondition (save a
//     key, ADR-0054);
//   - an unknown thread is CodeNotFound;
//   - a classified upstream rejection keeps its classification (shared with
//     the assist/maps surfaces);
//   - anything left is logged raw and returned as a static CodeUnavailable —
//     provider failures are the expected failure mode here, not an internal
//     fault, and a retry may succeed.
func (s *ChatServer) chatExchangeErr(threadID uuid.UUID, err error) *connect.Error {
	switch {
	case errors.Is(err, planchat.ErrAllowanceExhausted):
		return connect.NewError(connect.CodeResourceExhausted,
			errors.New("this month's included usage is exhausted; the chat resumes next month or after a plan upgrade"))
	case errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("no LLM provider is available: save an API key in Configuration or choose a plan that includes usage"))
	case errors.Is(err, storage.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("thread not found"))
	}
	if cerr := upstreamErr("PlanningExchange", chatExchangeSubject, err); cerr != nil {
		return cerr
	}
	s.log.Error("PlanningExchange: exchange failed", "thread_id", threadID, "err", err)
	return connect.NewError(connect.CodeUnavailable,
		errors.New("the planning chat is unavailable right now — try again in a moment"))
}

// toProtoPlanningThread maps a storage thread onto the wire.
func toProtoPlanningThread(t storage.PlanningThread) *managementv1.PlanningThread {
	return &managementv1.PlanningThread{
		Id:         t.ID.String(),
		CampaignId: t.CampaignID.String(),
		Title:      t.Title,
		CreatedAt:  timestamppb.New(t.CreatedAt),
		UpdatedAt:  timestamppb.New(t.UpdatedAt),
	}
}

// toProtoPlanningMessage maps a storage message onto the wire.
func toProtoPlanningMessage(m storage.PlanningMessage) *managementv1.PlanningMessage {
	return &managementv1.PlanningMessage{
		Id:        m.ID.String(),
		Role:      m.Role,
		Content:   m.Content,
		CreatedAt: timestamppb.New(m.CreatedAt),
	}
}
