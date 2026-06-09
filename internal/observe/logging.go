package observe

import (
	"context"
	"io"
	"log/slog"
)

// LogFormat selects the slog output encoding (ADR-0032: JSON in prod, text in
// dev). It replaces cmd/glyphoxa/main.go's hardcoded TextHandler.
type LogFormat string

const (
	// LogText is the human-readable dev encoding (slog.TextHandler).
	LogText LogFormat = "text"
	// LogJSON is the structured prod encoding (slog.JSONHandler), the one a log
	// pipeline ingests.
	LogJSON LogFormat = "json"
)

// ParseLogFormat maps a string (e.g. the -log-format flag or $GLYPHOXA_LOG_FORMAT)
// to a LogFormat, defaulting to text for any unrecognised value so a typo never
// silently swallows logs into JSON no one is reading.
func ParseLogFormat(s string) LogFormat {
	if LogFormat(s) == LogJSON {
		return LogJSON
	}
	return LogText
}

// NewLogger builds the process logger for the given format and level, with the
// disgo DAVE-decrypt noise already filtered (NewDAVEFilterHandler wrapping the
// chosen encoder). onDAVEDecrypt is the metric hook for
// glyphoxa_voice_dave_decrypt_errors_total (nil = no-op until the Prometheus
// adapter wires it in task #3). w is the sink (os.Stderr in main).
//
// The caller is expected to slog.SetDefault this logger AND pass it to disgo via
// bot.WithLogger so every library on the default logger — not just disgo's bot
// logger — is covered (observability.md §1.5).
func NewLogger(w io.Writer, format LogFormat, level slog.Level, onDAVEDecrypt func()) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var enc slog.Handler
	switch format {
	case LogJSON:
		enc = slog.NewJSONHandler(w, opts)
	default:
		enc = slog.NewTextHandler(w, opts)
	}
	return slog.New(NewDAVEFilterHandler(enc, onDAVEDecrypt))
}

// loggerKey is the private context key carrying a turn/session-scoped logger.
type loggerKey struct{}

// WithLogger returns a context carrying log, so downstream stages read it via
// [CtxLogger] instead of threading a bare *slog.Logger through every call
// (ADR-0032). Stamp turn/session fields with log.With(...) before calling.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, log)
}

// CtxLogger returns the logger carried by ctx, or the default logger if none was
// set, so call sites never nil-check. This is the ctxLogger(ctx) helper the A4
// cleanup introduces; stages resolve their logger from context and the
// turn/session fields ride along automatically.
func CtxLogger(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && log != nil {
		return log
	}
	return slog.Default()
}
