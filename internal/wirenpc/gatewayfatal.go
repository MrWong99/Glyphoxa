package wirenpc

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/disgoorg/disgo/rest"
	"github.com/gorilla/websocket"
)

// Fatal reason codes (#123). A FatalError.Reason is exactly one of these: a
// non-retryable Discord gateway/REST rejection the reconnect loop must NOT keep
// backing off against. They are part of the observable failure contract — the
// session's persisted end_reason and the SSE connection.state Detail carry them —
// so keep the set small and the values stable.
const (
	// ReasonInvalidBotToken: the Bot token was rejected. Gateway close 4004
	// (Authentication failed) or REST 401 (Unauthorized).
	ReasonInvalidBotToken = "invalid_bot_token"
	// ReasonDisallowedIntents: the Bot requested gateway intents it is not
	// approved for. Gateway close 4013 (invalid intents) / 4014 (disallowed
	// intents).
	ReasonDisallowedIntents = "disallowed_intents"
	// ReasonBotNotAuthorized: the Bot lacks authorization for the action. REST 403
	// (Forbidden).
	ReasonBotNotAuthorized = "bot_not_authorized"
	// ReasonGatewayRejected: the gateway rejected the session for a
	// non-authentication reason it will not accept on retry. Gateway close 4010
	// (invalid shard) / 4011 (sharding required) / 4012 (invalid API version).
	ReasonGatewayRejected = "gateway_rejected"
)

// FatalError marks a Voice Session connection failure that is TERMINAL, not
// transient: retrying can never succeed (a bad Bot token, disallowed intents, a
// gateway rejection), so the reconnect loop must stop and surface it instead of
// backing off forever (#123). runWithReconnect returns it immediately; the
// session Manager records the persisted status as failed with Reason.
//
// It wraps the originating error, so errors.As recovers it through the %w chains
// both client paths build (per-cycle acquireClient and the presence
// Ensure→ClientProvider), and errors.Is still matches the underlying cause.
type FatalError struct {
	// Reason is one of the reason codes above — the bounded, stable classification.
	Reason string
	// Err is the originating gateway/REST error, preserved for Unwrap so the full
	// %w chain (and its detail) survives.
	Err error
}

// Error renders "<reason>: <prose>" — the reason code followed by the underlying
// error's message, e.g. "invalid_bot_token: wirenpc: open gateway: websocket:
// close 4004: Authentication failed". It is the readable Detail the SSE
// connection.state{failed} frame and the persisted end_reason carry.
func (e *FatalError) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Err)
}

// Unwrap exposes the originating error so errors.Is/As traverse into the cause.
func (e *FatalError) Unwrap() error { return e.Err }

// classifyFatal inspects a connect-and-serve error and returns a *FatalError when
// it is a terminal, non-retryable gateway/REST rejection — else nil, meaning the
// failure is transient and the reconnect loop should keep backing off (#123).
//
// It matches through errors.As, so a rejection wrapped by both client paths' %w
// chains still classifies. A gorilla *websocket.CloseError with a non-reconnectable
// close code (disgo returns exactly these from OpenGateway) is fatal; a REST
// *rest.Error with a 401/403 status is fatal. Everything else — reconnectable
// closes, 429/5xx, network errors, nil — is transient.
func classifyFatal(err error) *FatalError {
	if err == nil {
		return nil
	}

	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case 4004: // Authentication failed
			return &FatalError{Reason: ReasonInvalidBotToken, Err: err}
		case 4013, 4014: // invalid / disallowed gateway intents
			return &FatalError{Reason: ReasonDisallowedIntents, Err: err}
		case 4010, 4011, 4012: // invalid shard / sharding required / invalid API version
			return &FatalError{Reason: ReasonGatewayRejected, Err: err}
		}
		return nil // reconnectable close (4000, 4009, …) — transient
	}

	var restErr *rest.Error
	if errors.As(err, &restErr) && restErr.Response != nil {
		switch restErr.Response.StatusCode {
		case http.StatusUnauthorized: // 401 — the token is not valid
			return &FatalError{Reason: ReasonInvalidBotToken, Err: err}
		case http.StatusForbidden: // 403 — the bot is not authorized
			return &FatalError{Reason: ReasonBotNotAuthorized, Err: err}
		}
		return nil // 429 / 5xx — transient
	}

	return nil
}
