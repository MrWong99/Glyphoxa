//go:build integration

// This test guards the issue-#69 credential fan-out — the final hop the resolver
// value-tests cannot see: buildConversation handing each resolved BYOK key to
// its OWN adapter. It needs a real Silero VAD (like the other buildConversation
// tests), so it is tag-isolated behind `integration` (ADR-0033); no Postgres is
// used. The keyless resolver logic lives in credentials_test.go.
package wirenpc

import (
	"io"
	"log/slog"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestBuildConversation_RoutesEachKeyToItsAdapter is the AC1 fan-out guard. The
// resolver output is value-tested elsewhere; this covers the untested final hop
// (keys.llm -> groq, keys.stt -> stt/elevenlabs, keys.tts -> tts/elevenlabs).
// A slot swap or a dropped `cfg.keys = keys` would silently revert the feature to
// ENV while the rest of the suite (every call passes providerKeys{}) stayed
// green. The adapters expose no key getter, so we spy on the package-var
// constructors with three DISTINCT non-empty keys and assert each reached its
// own adapter (catches both a swap and an empty-from-dropped-assignment).
func TestBuildConversation_RoutesEachKeyToItsAdapter(t *testing.T) {
	origLLM, origSTT, origTTS := newLLM, newSTT, newTTS
	t.Cleanup(func() { newLLM, newSTT, newTTS = origLLM, origSTT, origTTS })

	var gotLLM, gotSTT, gotTTS string
	newLLM = func(apiKey string, opts ...groq.Option) *groq.Client {
		gotLLM = apiKey
		return origLLM(apiKey, opts...)
	}
	newSTT = func(apiKey string, opts ...stteleven.Option) *stteleven.Client {
		gotSTT = apiKey
		return origSTT(apiKey, opts...)
	}
	newTTS = func(apiKey string, opts ...ttseleven.Option) *ttseleven.Client {
		gotTTS = apiKey
		return origTTS(apiKey, opts...)
	}

	keys := providerKeys{llm: "L-llm-key", stt: "S-stt-key", tts: "T-tts-key"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _, cleanup, err := buildConversation(voiceevent.NewBus(), log,
		[]npcSpec{hardcodedNPC()}, "", ttseleven.New(""), nil, keys, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildConversation: %v", err)
	}
	defer cleanup()

	if gotLLM != keys.llm {
		t.Errorf("groq adapter got apiKey %q, want %q — keys.llm must reach the LLM adapter, not another slot or ENV", gotLLM, keys.llm)
	}
	if gotSTT != keys.stt {
		t.Errorf("stt adapter got apiKey %q, want %q — keys.stt must reach the STT adapter", gotSTT, keys.stt)
	}
	if gotTTS != keys.tts {
		t.Errorf("tts adapter got apiKey %q, want %q — keys.tts must reach the TTS adapter", gotTTS, keys.tts)
	}
}
