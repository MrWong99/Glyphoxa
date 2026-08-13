package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// These tests pin the #592 interceptor upgrade: the policy stack is a full
// connect.Interceptor whose WrapStreamingHandler gates server-streaming
// procedures (the chat exchange is the first) with the SAME session/CSRF/
// tenant policy as every unary call. Before the upgrade a streaming procedure
// mounted on the stack was served with NO check at all — a
// UnaryInterceptorFunc's streaming wrap is a pass-through.

// streamProbe is a ChatService with only the streaming procedure implemented:
// it records whether the policy injected the operator + tenant, then ends the
// stream cleanly.
type streamProbe struct {
	managementv1connect.UnimplementedChatServiceHandler
	ran       bool
	hadUser   bool
	hadTenant bool
}

func (p *streamProbe) PlanningExchange(
	ctx context.Context,
	_ *connect.Request[managementv1.PlanningExchangeRequest],
	stream *connect.ServerStream[managementv1.PlanningExchangeResponse],
) error {
	p.ran = true
	_, p.hadUser = auth.CurrentUser(ctx)
	_, p.hadTenant = auth.TenantID(ctx)
	return stream.Send(&managementv1.PlanningExchangeResponse{})
}

// newStreamProbeClient mounts the probe behind the REAL policy stack.
func newStreamProbeClient(t *testing.T) (managementv1connect.ChatServiceClient, *streamProbe) {
	t.Helper()
	probe := &streamProbe{}
	op := operator()
	stack := auth.NewStack(
		fakeAuthN{users: map[string]storage.User{validToken: op}},
		fakeTenant{id: uuid.New()},
	)
	mux := http.NewServeMux()
	mux.Handle(managementv1connect.NewChatServiceHandler(probe, stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewChatServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON()), probe
}

// drainStream consumes the stream and returns its terminal error.
func drainStream(stream *connect.ServerStreamForClient[managementv1.PlanningExchangeResponse]) error {
	for stream.Receive() {
	}
	return stream.Err()
}

func exchangeReq(withSession, withCSRF bool) *connect.Request[managementv1.PlanningExchangeRequest] {
	req := connect.NewRequest(&managementv1.PlanningExchangeRequest{
		CampaignId: "irrelevant", ThreadId: "irrelevant", Message: "hi",
	})
	cookies := ""
	if withSession {
		cookies = auth.SessionCookieName + "=" + validToken
	}
	if withCSRF {
		if cookies != "" {
			cookies += "; "
		}
		cookies += auth.CSRFCookieName + "=csrf-token-value"
		req.Header().Set("X-CSRF-Token", "csrf-token-value")
	}
	if cookies != "" {
		req.Header().Set("Cookie", cookies)
	}
	return req
}

func TestStreamingHandler_NoSession_Unauthenticated(t *testing.T) {
	t.Parallel()
	client, probe := newStreamProbeClient(t)
	stream, err := client.PlanningExchange(context.Background(), exchangeReq(false, false))
	if err == nil {
		err = drainStream(stream)
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", err)
	}
	if probe.ran {
		t.Fatalf("handler ran despite missing session")
	}
}

func TestStreamingHandler_NoCSRF_PermissionDenied(t *testing.T) {
	t.Parallel()
	client, probe := newStreamProbeClient(t)
	// A Connect stream open is a POST and PlanningExchange is state-changing:
	// the double-submit CSRF check applies exactly as to a unary mutation.
	stream, err := client.PlanningExchange(context.Background(), exchangeReq(true, false))
	if err == nil {
		err = drainStream(stream)
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", err)
	}
	if probe.ran {
		t.Fatalf("handler ran despite missing CSRF token")
	}
}

func TestStreamingHandler_FullPolicy_InjectsIdentity(t *testing.T) {
	t.Parallel()
	client, probe := newStreamProbeClient(t)
	stream, err := client.PlanningExchange(context.Background(), exchangeReq(true, true))
	if err == nil {
		err = drainStream(stream)
	}
	if err != nil {
		t.Fatalf("authorized stream failed: %v", err)
	}
	if !probe.ran || !probe.hadUser || !probe.hadTenant {
		t.Fatalf("ran=%v hadUser=%v hadTenant=%v, want all true", probe.ran, probe.hadUser, probe.hadTenant)
	}
}
