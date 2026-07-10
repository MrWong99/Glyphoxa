package highlight

// discardWriter is an io.Writer that drops everything, used to build a no-op slog
// handler when the caller passes a nil logger (mirrors internal/tape).
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
