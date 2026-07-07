package wirenpc

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/rest"
	"github.com/gorilla/websocket"
)

// restErr builds a disgo *rest.Error carrying an HTTP status, the way the REST
// client surfaces a Discord API rejection (401/403/…).
func restErr(status int) *rest.Error {
	return &rest.Error{Response: &http.Response{StatusCode: status}}
}

// TestClassifyFatal maps gateway/REST rejections onto their fatal reason, and
// leaves transient failures (reconnectable closes, rate limits, 5xx, net, nil)
// unclassified so the reconnect loop keeps backing off (#123).
func TestClassifyFatal(t *testing.T) {
	t.Parallel()

	// A 4004 wrapped through the presence + open-gateway %w chain, mimicking how
	// the error actually reaches the classifier (acquireClient / Ensure wrap it).
	wrapped4004 := fmt.Errorf("wirenpc: standing Discord client unavailable: %w",
		fmt.Errorf("wirenpc: open gateway: %w", &websocket.CloseError{Code: 4004, Text: "Authentication failed"}))

	tests := []struct {
		name string
		err  error
		want string // "" means classifyFatal must return nil (transient)
	}{
		{"close 4004 wrapped -> invalid_bot_token", wrapped4004, ReasonInvalidBotToken},
		{"close 4013 -> disallowed_intents", &websocket.CloseError{Code: 4013}, ReasonDisallowedIntents},
		{"close 4014 -> disallowed_intents", &websocket.CloseError{Code: 4014}, ReasonDisallowedIntents},
		{"close 4010 -> gateway_rejected", &websocket.CloseError{Code: 4010}, ReasonGatewayRejected},
		{"close 4011 -> gateway_rejected", &websocket.CloseError{Code: 4011}, ReasonGatewayRejected},
		{"close 4012 -> gateway_rejected", &websocket.CloseError{Code: 4012}, ReasonGatewayRejected},
		{"close 4000 reconnectable -> transient", &websocket.CloseError{Code: 4000}, ""},
		{"close 4009 reconnectable -> transient", &websocket.CloseError{Code: 4009}, ""},
		{"plain net error -> transient", errors.New("dial tcp: connection refused"), ""},
		{"nil -> transient", nil, ""},
		{"rest 401 -> invalid_bot_token", restErr(http.StatusUnauthorized), ReasonInvalidBotToken},
		{"rest 403 -> bot_not_authorized", restErr(http.StatusForbidden), ReasonBotNotAuthorized},
		{"rest 429 -> transient", restErr(http.StatusTooManyRequests), ""},
		{"rest 500 -> transient", restErr(http.StatusInternalServerError), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyFatal(tt.err)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("classifyFatal(%v) = %+v, want nil (transient)", tt.err, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("classifyFatal(%v) = nil, want reason %q", tt.err, tt.want)
			}
			if got.Reason != tt.want {
				t.Errorf("classifyFatal reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

// TestFatalError_ErrorAndUnwrap pins the readable "<reason>: <prose>" message and
// that errors.As recovers a *FatalError through a further %w wrap — the exact read
// the session Manager does to record the failed reason (#123).
func TestFatalError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()

	cause := &websocket.CloseError{Code: 4004, Text: "Authentication failed"}
	fe := classifyFatal(fmt.Errorf("wirenpc: open gateway: %w", cause))
	if fe == nil {
		t.Fatal("classifyFatal returned nil for a 4004")
	}
	if !strings.HasPrefix(fe.Error(), ReasonInvalidBotToken+": ") {
		t.Errorf("FatalError.Error() = %q, want %q prefix", fe.Error(), ReasonInvalidBotToken+": ")
	}
	if !errors.Is(fe, cause) {
		t.Errorf("errors.Is(FatalError, cause) = false, want the CloseError in the chain")
	}

	// The Manager wraps the loop error again before recording it; errors.As must
	// still recover the classification from that outer chain.
	outer := fmt.Errorf("session: loop exited: %w", error(fe))
	var recovered *FatalError
	if !errors.As(outer, &recovered) || recovered.Reason != ReasonInvalidBotToken {
		t.Errorf("errors.As through outer wrap = %v/%+v, want *FatalError invalid_bot_token", errors.As(outer, &recovered), recovered)
	}
}
