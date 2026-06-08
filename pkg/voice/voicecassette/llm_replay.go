//go:build !record

package voicecassette

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// LoadLLM reads tests/voice-cassettes/<name>.yaml and returns an [llm.Provider]
// that replays it.
//
// Default (replay) build: returns a [*LLMProvider] that hashes each
// [llm.Request] it is handed and replays the recorded exchange for that hash
// (text, tool_calls, stop reason). Missing, malformed, or empty cassettes — and
// any unrecorded prompt hash — fail the test, so a prompt change is caught per
// ADR-0021. To rewrite a cassette against a live LLM, rebuild with
// `-tags=record` — see llm_record.go.
func LoadLLM(t *testing.T, name string) llm.Provider {
	t.Helper()
	c, _ := loadLLMCassetteFromDisk(t, name, true)
	return &LLMProvider{name: name, byHash: indexByHash(c)}
}
