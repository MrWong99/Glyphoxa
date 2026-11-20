package rpc

import (
	"errors"
	"log/slog"

	"connectrpc.com/connect"
)

// internalErr is the RPC tier's redaction posture in one call: the detail
// (msg, err and any extra slog key/value pairs) goes to the server log ONCE,
// and the client gets the generic CodeInternal "internal error" — no storage
// text, SQL or provider body ever reaches the wire. It replaces the
// two-line "log, then connect.NewError(CodeInternal, "internal error")"
// pattern the handlers repeated.
func internalErr(log *slog.Logger, msg string, err error, kv ...any) *connect.Error {
	log.Error(msg, append(kv, "err", err)...)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
