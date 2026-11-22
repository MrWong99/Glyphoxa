package wirenpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// batchOnlyRecognizer implements only the batch [stt.Recognizer] — no streaming.
type batchOnlyRecognizer struct{}

func (batchOnlyRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{}, nil
}

// streamingRecognizer implements both the batch and streaming interfaces, like the
// real ElevenLabs Client (ADR-0042).
type streamingRecognizer struct{ batchOnlyRecognizer }

func (streamingRecognizer) OpenStream(context.Context, stt.StreamConfig) (stt.Stream, error) {
	return nil, nil
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestBuildStreamManager_Gating pins the selection seam (issue #180, C6): a manager
// is wired only when streaming is enabled AND the recognizer supports streaming;
// otherwise the byte-for-byte batch path (nil manager) is kept.
func TestBuildStreamManager_Gating(t *testing.T) {
	if got := buildStreamManager(streamingRecognizer{}, true, observe.Discard{}, discardLog()); got == nil {
		t.Error("streaming enabled + a streaming recognizer must wire a manager")
	}
	if got := buildStreamManager(streamingRecognizer{}, false, observe.Discard{}, discardLog()); got != nil {
		t.Error("streaming disabled must not wire a manager, even for a streaming recognizer")
	}
	if got := buildStreamManager(batchOnlyRecognizer{}, true, observe.Discard{}, discardLog()); got != nil {
		t.Error("a batch-only recognizer must not wire a manager, even with streaming enabled")
	}
}

// TestBuildStreamManager_WarnsOnUnsupported pins that opting into streaming with a
// batch-only provider logs a warning (not a silent fall-through), so the operator
// learns why the batch path is in effect.
func TestBuildStreamManager_WarnsOnUnsupported(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if got := buildStreamManager(batchOnlyRecognizer{}, true, observe.Discard{}, log); got != nil {
		t.Fatal("batch-only recognizer must not wire a manager")
	}
	if !strings.Contains(buf.String(), "does not support it") {
		t.Errorf("expected a warning about the unsupported provider, got log: %q", buf.String())
	}

	// The supported path must stay quiet — no spurious warning.
	buf.Reset()
	buildStreamManager(streamingRecognizer{}, true, observe.Discard{}, log)
	if buf.Len() != 0 {
		t.Errorf("a supported streaming recognizer must not warn, got log: %q", buf.String())
	}
}

// --- #702: the stt_final series and the stream log through the production wiring ---

// liveStream is a working [stt.Stream]: every commit resolves with "hi".
type liveStream struct{}

func (liveStream) Send(audio.Frame) error { return nil }
func (liveStream) Commit() (<-chan stt.CommitResult, error) {
	ch := make(chan stt.CommitResult, 1)
	ch <- stt.CommitResult{Transcript: stt.Transcript{Text: "hi"}}
	return ch, nil
}
func (liveStream) Close() error { return nil }

// liveRecognizer streams (unless openErr is set) and also answers the batch path.
type liveRecognizer struct {
	batchOnlyRecognizer
	openErr error
}

func (r liveRecognizer) OpenStream(context.Context, stt.StreamConfig) (stt.Stream, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	return liveStream{}, nil
}

// utteranceVAD scripts one short utterance per four frames, forever: speech
// start, continue, speech end, silence.
type utteranceVAD struct{ i int }

func (v *utteranceVAD) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	typ := [...]vad.VADEventType{vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd, vad.VADSilence}[v.i%4]
	v.i++
	return vad.VADEvent{Type: typ}, nil
}
func (*utteranceVAD) Reset()       {}
func (*utteranceVAD) Close() error { return nil }

func scrapeMetrics(t *testing.T, rec *observe.PrometheusRecorder) string {
	t.Helper()
	srv := httptest.NewServer(rec.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// speakUtterances feeds n scripted utterances through conv and drains them.
func speakUtterances(t *testing.T, conv *orchestrator.Conversation, n int) {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 * n {
		if err := conv.Feed(f); err != nil {
			t.Fatalf("Feed: %v", err)
		}
	}
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// TestSTTFinal_ReachesPrometheusThroughTheProductionWiring is the shipped-dead-
// metric guard for #702. It builds the STT half of the live pipeline the way
// buildConversation does — the batch stage and the manager from
// buildStreamManager, both recording into observe.TeeUsage over the real
// Prometheus adapter, which is what the session Manager hands down — drives real
// utterances through an orchestrator.Conversation, and reads the series off the
// registry. A spy would pass even if the tee dropped the call.
func TestSTTFinal_ReachesPrometheusThroughTheProductionWiring(t *testing.T) {
	series := func(transport, reason string) string {
		return `glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="` + reason + `",transport="` + transport + `"}`
	}
	build := func(t *testing.T, rec stt.Recognizer, streaming bool) (*observe.PrometheusRecorder, *orchestrator.Conversation) {
		t.Helper()
		prom := observe.NewPrometheusRecorder()
		teed := observe.TeeUsage(prom, nopUsageSink{})
		bus := voiceevent.NewBus()
		sttStage := orchestrator.NewSTT(bus, rec, orchestrator.WithSTTMetrics(teed, observe.ProviderElevenLabs))
		conv, err := orchestrator.NewConversation(bus, orchestrator.NewVAD(bus, &utteranceVAD{}), sttStage, nil,
			orchestrator.WithStreamingSTT(buildStreamManager(rec, streaming, teed, discardLog())))
		if err != nil {
			t.Fatalf("NewConversation: %v", err)
		}
		t.Cleanup(conv.Register(t.Context()))
		return prom, conv
	}

	t.Run("streaming on", func(t *testing.T) {
		prom, conv := build(t, liveRecognizer{}, true)
		// The default lane dials on its first speech, so utterance 1 is a fallback
		// and the following ones stream once the session is up.
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(scrapeMetrics(t, prom), series("stream", "committed")) {
			if time.Now().After(deadline) {
				t.Fatalf("no streamed final ever reached the registry:\n%s", sttFinalLines(scrapeMetrics(t, prom)))
			}
			speakUtterances(t, conv, 1)
		}
		out := scrapeMetrics(t, prom)
		if !strings.Contains(out, series("batch_fallback", "stream_down")+" ") {
			t.Errorf("the first utterance's fallback is missing:\n%s", sttFinalLines(out))
		}
		if strings.Contains(out, `transport="batch"`) {
			t.Errorf("a streaming lane was also counted as plain batch:\n%s", sttFinalLines(out))
		}
	})

	t.Run("streaming off", func(t *testing.T) {
		prom, conv := build(t, liveRecognizer{}, false)
		speakUtterances(t, conv, 2)
		out := scrapeMetrics(t, prom)
		if want := series("batch", "streaming_off") + " 2"; !strings.Contains(out, want) {
			t.Errorf("scrape missing %q:\n%s", want, sttFinalLines(out))
		}
	})
}

func sttFinalLines(scrape string) string {
	var b strings.Builder
	for _, line := range strings.Split(scrape, "\n") {
		if strings.Contains(line, "stt_final") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// TestStreamManagers_LogDialFailures: both managers the pipeline builds — the
// default lane's and every Speaker Lane's — carry the pipeline logger, so a
// keyless or mis-keyed deployment says why it is on batch STT (#702).
func TestStreamManagers_LogDialFailures(t *testing.T) {
	rec := liveRecognizer{openErr: &stt.StreamError{Code: "auth_error", Fatal: true, Err: errors.New("missing API key")}}

	t.Run("speaker lane", func(t *testing.T) {
		logs := &safeBuffer{}
		factory := buildLaneStreamFactory(rec, true, observe.Discard{}, slog.New(slog.NewTextHandler(logs, nil)))
		bus := voiceevent.NewBus()
		conv, err := orchestrator.NewConversation(bus, orchestrator.NewVAD(bus, &utteranceVAD{}),
			orchestrator.NewSTT(bus, rec), nil,
			orchestrator.WithSpeakerLanes(func() (*orchestrator.VAD, func(), error) {
				return orchestrator.NewVAD(bus, &utteranceVAD{}), func() {}, nil
			}),
			orchestrator.WithLaneStreamingSTT(factory, 4))
		if err != nil {
			t.Fatalf("NewConversation: %v", err)
		}
		t.Cleanup(conv.Register(t.Context()))

		f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
		if err != nil {
			t.Fatal(err)
		}
		if err := conv.Feed(f.WithSpeaker("speaker-1")); err != nil { // opens the lane, which dials
			t.Fatalf("Feed: %v", err)
		}
		waitForLog(t, logs, "missing API key")
	})

	t.Run("default lane", func(t *testing.T) {
		logs := &safeBuffer{}
		sm := buildStreamManager(rec, true, observe.Discard{}, slog.New(slog.NewTextHandler(logs, nil)))
		bus := voiceevent.NewBus()
		conv, err := orchestrator.NewConversation(bus, orchestrator.NewVAD(bus, &utteranceVAD{}),
			orchestrator.NewSTT(bus, rec), nil, orchestrator.WithStreamingSTT(sm))
		if err != nil {
			t.Fatalf("NewConversation: %v", err)
		}
		t.Cleanup(conv.Register(t.Context()))
		speakUtterances(t, conv, 1) // first speech triggers the on-demand dial
		waitForLog(t, logs, "missing API key")
	})
}

func waitForLog(t *testing.T, logs *safeBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log never mentioned %q:\n%s", want, logs.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// safeBuffer is a goroutine-safe log sink.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
