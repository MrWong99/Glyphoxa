package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// TestHandlerOptions_BoundRequestBodies pins the read cap every guarded service
// inherits from the stack: a message over MaxRequestBytes is refused with
// CodeResourceExhausted before any handler (or interceptor) sees it, while a
// message under the cap goes through. A zero Stack carries no interceptors, so
// the test isolates the cap itself.
func TestHandlerOptions_BoundRequestBodies(t *testing.T) {
	t.Parallel()
	const proc = "/glyphoxa.test.Echo/Echo"
	mux := http.NewServeMux()
	mux.Handle(proc, connect.NewUnaryHandler(proc,
		func(_ context.Context, req *connect.Request[managementv1.CreateMapRequest]) (*connect.Response[managementv1.CreateMapResponse], error) {
			return connect.NewResponse(&managementv1.CreateMapResponse{}), nil
		}, (&auth.Stack{}).HandlerOptions()...))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := connect.NewClient[managementv1.CreateMapRequest, managementv1.CreateMapResponse](srv.Client(), srv.URL+proc)

	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
		ImageBytes: make([]byte, 1<<20),
	})); err != nil {
		t.Fatalf("1 MiB message: %v, want success", err)
	}
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
		ImageBytes: make([]byte, auth.MaxRequestBytes+1),
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized message: err = %v, want ResourceExhausted", err)
	}
}
