package wirenpc

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// sttRequest is what one STT request carried, on either transport.
type sttRequest struct {
	keyterms []string
	language []string
}

// fakeScribe stands in for the ElevenLabs STT API on both transports: the batch
// POST and the realtime websocket. It records the hints each request carried and
// answers every utterance with "hi".
type fakeScribe struct {
	*httptest.Server
	mu      sync.Mutex
	batches []sttRequest
	dials   []sttRequest
	commits int
	// refuseKeyterms makes the provider refuse any request that carries keyterms,
	// each transport in its own way: a 400 for the batch POST, an invalid_request
	// frame in place of session_started for a realtime session.
	refuseKeyterms bool
}

func newFakeScribe(t *testing.T) *fakeScribe {
	t.Helper()
	f := &fakeScribe{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/speech-to-text", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.batches = append(f.batches, sttRequest{
			keyterms: r.MultipartForm.Value["keyterms"],
			language: r.MultipartForm.Value["language_code"],
		})
		refuse := f.refuseKeyterms && len(r.MultipartForm.Value["keyterms"]) > 0
		f.mu.Unlock()
		if refuse {
			http.Error(w, `{"detail":{"status":"invalid_keyword_length","param":"keywords"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"text":"hi"}`))
	})
	mux.HandleFunc("/v1/speech-to-text/realtime", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.dials = append(f.dials, sttRequest{keyterms: q["keyterms"], language: q["language_code"]})
		refuse := f.refuseKeyterms && len(q["keyterms"]) > 0
		f.mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if refuse {
			_ = conn.WriteJSON(map[string]any{"message_type": "invalid_request", "error": "Total length of keyterms cannot exceed 1000 characters."})
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Commit bool `json:"commit"`
			}
			if json.Unmarshal(data, &msg) != nil || !msg.Commit {
				continue
			}
			f.mu.Lock()
			f.commits++
			f.mu.Unlock()
			if conn.WriteJSON(map[string]any{"message_type": "committed_transcript", "text": "hi"}) != nil {
				return
			}
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeScribe) snapshot() (batches, dials []sttRequest, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sttRequest(nil), f.batches...), append([]sttRequest(nil), f.dials...), f.commits
}

// pointSTTAt makes the pipeline's STT constructor build the REAL ElevenLabs
// client — with whatever options the wiring passes it — against srv.
func pointSTTAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := newSTT
	t.Cleanup(func() { newSTT = orig })
	newSTT = func(_ string, opts ...stteleven.Option) *stteleven.Client {
		return orig("test-key", append(opts, stteleven.WithBaseURL(srv.URL))...)
	}
}

// hintedSession is a session Config with a Cast, a Butler and player characters.
func hintedSession() Config {
	return Config{
		STTStreaming: true,
		language:     "de",
		npcs: []npcSpec{
			{agentID: "flo", name: "Flo", aliases: []string{"Florian"}},
			{agentID: "wolf", name: "Wolf"}, // an everyday German word: never a keyterm
			{agentID: "butler", name: "Glyphoxa", role: string(storage.AgentRoleButler), addressOnly: true},
		},
		playerCharacterTerms: []string{"Artusas", "Art"},
	}
}

// speakSTT drives the session's STT wiring the way a Voice Session does: the
// config goes through the session-start step and the per-cycle deps, the STT
// stages come from the pipeline's own constructor, and utterances are spoken on
// the default lane AND on a Speaker Lane until the fake provider has seen a batch
// request, a streamed commit and both lanes' dials.
func speakSTT(t *testing.T, cfg Config, scribe *fakeScribe) (batches, dials []sttRequest) {
	t.Helper()
	return speakSTTLogged(t, cfg, scribe, slog.New(slog.DiscardHandler))
}

// speakSTTLogged is [speakSTT] with the session's logger supplied.
func speakSTTLogged(t *testing.T, cfg Config, scribe *fakeScribe, log *slog.Logger) (batches, dials []sttRequest) {
	t.Helper()
	pointSTTAt(t, scribe.Server)
	cfg = sessionConfig(cfg, log)
	bus := voiceevent.NewBus()
	deps := cycleConversationDeps(bus, log, cfg, nil, &fakeCyclePump{}, nil)
	wiring := buildSTT(deps, observe.Discard{}, retry.Policy{})

	conv, err := orchestrator.NewConversation(bus, orchestrator.NewVAD(bus, &utteranceVAD{}), wiring.stage, nil,
		orchestrator.WithStreamingSTT(wiring.stream),
		orchestrator.WithSpeakerLanes(func() (*orchestrator.VAD, func(), error) {
			return orchestrator.NewVAD(bus, &utteranceVAD{}), func() {}, nil
		}),
		orchestrator.WithLaneStreamingSTT(wiring.laneStreams, 4))
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	t.Cleanup(conv.Register(t.Context()))

	frame, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatal(err)
	}
	wantDials := 0
	if cfg.STTStreaming {
		wantDials = 2 // the default lane's and the Speaker Lane's
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		speakUtterances(t, conv, 1) // default lane: the first one is a batch fallback
		for range 4 {               // one utterance on a Speaker Lane
			if err := conv.Feed(frame.WithSpeaker("speaker-1")); err != nil {
				t.Fatalf("Feed: %v", err)
			}
		}
		if err := conv.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		b, d, commits := scribe.snapshot()
		if len(b) > 0 && len(d) >= wantDials && (commits > 0 || !cfg.STTStreaming) {
			return b, d
		}
		if time.Now().After(deadline) {
			t.Fatalf("the provider saw %d batch requests, %d dials, %d streamed commits; want all three", len(b), len(d), commits)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSTTHints_ReachBothTransportsOfOneSession is the wiring proof for #701: with
// keyterms and the language hint switched on, the session's term list — built
// from its Cast, Butler and player characters — is on EVERY request the real
// ElevenLabs client makes for that session: each realtime dial (the default
// lane's and a Speaker Lane's) and each batch POST, which with streaming on is
// the per-utterance batch fallback. A list that reached only one transport would
// transcribe the same name two ways depending on which path an utterance took.
func TestSTTHints_ReachBothTransportsOfOneSession(t *testing.T) {
	cfg := hintedSession()
	cfg.STTKeyterms = true
	cfg.STTLanguageHint = true
	batches, dials := speakSTT(t, cfg, newFakeScribe(t))

	want := sttRequest{keyterms: []string{"Flo", "Florian", "Glyphoxa", "Artusas", "Art"}, language: []string{"de"}}
	for i, got := range batches {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("batch request %d carried %+v, want %+v", i, got, want)
		}
	}
	for i, got := range dials {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("realtime dial %d carried %+v, want %+v", i, got, want)
		}
	}
}

// TestSTTHints_ProviderRefusesTheList_SessionKeepsHearing is the fail-open proof
// through the session's real wiring (#701 review). The provider refuses the
// session's keyterm list on both transports. Nothing is lost: the utterances are
// still transcribed — the batch one in the same call, the streamed ones after a
// redial — the session stops offering the list, and the operator gets ONE
// warning on the session's own logger, without the names in it.
func TestSTTHints_ProviderRefusesTheList_SessionKeepsHearing(t *testing.T) {
	cfg := hintedSession()
	cfg.STTKeyterms = true
	scribe := newFakeScribe(t)
	scribe.refuseKeyterms = true
	logs := &safeBuffer{}
	// speakSTTLogged returns only once a batch request AND a streamed commit were
	// answered, which a refused list would prevent on both transports.
	batches, dials := speakSTTLogged(t, cfg, scribe,
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))

	offered := false
	for _, r := range append(batches, dials...) {
		offered = offered || len(r.keyterms) > 0
	}
	if !offered {
		t.Fatal("the session never offered its keyterms, so nothing was refused")
	}
	if last := batches[len(batches)-1]; len(last.keyterms) != 0 {
		t.Errorf("the last batch request still carried the refused keyterms %q", last.keyterms)
	}
	if last := dials[len(dials)-1]; len(last.keyterms) != 0 {
		t.Errorf("the last realtime dial still carried the refused keyterms %q", last.keyterms)
	}

	out := logs.String()
	if n := strings.Count(out, "provider refused the keyterm list"); n != 1 {
		t.Errorf("want one refusal warning on the session's logger, got %d:\n%s", n, out)
	}
	for _, name := range []string{"Florian", "Artusas", "Glyphoxa"} {
		if strings.Contains(out, name) {
			t.Errorf("a warning names a term (%q):\n%s", name, out)
		}
	}
}

// TestSTTHints_OffByDefaultInAConfig: a Config that asks for neither sends
// neither, on either transport — the pre-#701 requests, and what
// GLYPHOXA_STT_KEYTERMS=0 restores.
func TestSTTHints_OffByDefaultInAConfig(t *testing.T) {
	batches, dials := speakSTT(t, hintedSession(), newFakeScribe(t))
	for i, got := range append(batches, dials...) {
		if len(got.keyterms) != 0 || len(got.language) != 0 {
			t.Errorf("request %d carried hints %+v with both switches off", i, got)
		}
	}
}

// TestSTTHints_BatchOnlySession: with streaming off the list still reaches the
// batch transport, the only one left.
func TestSTTHints_BatchOnlySession(t *testing.T) {
	cfg := hintedSession()
	cfg.STTStreaming = false
	cfg.STTKeyterms = true
	batches, dials := speakSTT(t, cfg, newFakeScribe(t))
	if len(dials) != 0 {
		t.Fatalf("streaming is off but %d realtime sessions were dialled", len(dials))
	}
	for i, got := range batches {
		if want := []string{"Flo", "Florian", "Glyphoxa", "Artusas", "Art"}; !reflect.DeepEqual(got.keyterms, want) {
			t.Errorf("batch request %d keyterms = %q, want %q", i, got.keyterms, want)
		}
		if len(got.language) != 0 {
			t.Errorf("batch request %d sent language_code %q without the opt-in", i, got.language)
		}
	}
}

// TestSessionConfig_LogsTheTermCountOnce: session start says how many keyterms
// the session sends — and not which, since the list is the players' names.
func TestSessionConfig_LogsTheTermCountOnce(t *testing.T) {
	logs := &safeBuffer{}
	cfg := hintedSession()
	cfg.STTKeyterms = true
	sessionConfig(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))

	out := logs.String()
	if n := strings.Count(out, "stt keyterms"); n != 1 {
		t.Fatalf("want one keyterm line at session start, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "terms=5") {
		t.Errorf("the line must carry the term count (5):\n%s", out)
	}
	for _, name := range []string{"Florian", "Artusas", "Glyphoxa"} {
		if strings.Contains(out, name) {
			t.Errorf("the info log names a term (%q):\n%s", name, out)
		}
	}
}
