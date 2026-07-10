package highlight

import (
	"sync"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// spyRecorder is an observe.StageRecorder that captures the LLM token usage the
// detector meters; every other method is a no-op (embedded Discard).
type spyRecorder struct {
	observe.Discard
	mu    sync.Mutex
	seen  bool
	in    int
	out   int
	model string
}

func (r *spyRecorder) LLMTokens(_ observe.Provider, model string, in, out int) {
	r.mu.Lock()
	r.seen = true
	r.in, r.out, r.model = in, out, model
	r.mu.Unlock()
}

func (r *spyRecorder) llmTokens() (in, out int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.in, r.out, r.seen
}
