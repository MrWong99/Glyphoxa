// Package elevenlabs provides an ElevenLabs Scribe v2 Realtime STT provider
// using the ElevenLabs streaming WebSocket API. It implements the stt.Provider
// interface.
package elevenlabs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/coder/websocket"
)

const (
	defaultBaseURL    = "wss://api.elevenlabs.io/v1/speech-to-text/realtime"
	defaultModel      = "scribe_v2_realtime"
	defaultLanguage   = "en"
	defaultSampleRate = 16000

	closeTimeout = 5 * time.Second
)

// Option is a functional option for configuring the ElevenLabs Provider.
type Option func(*Provider)

// WithModel sets the ElevenLabs model to use (e.g., "scribe_v2_realtime").
func WithModel(model string) Option {
	return func(p *Provider) {
		p.model = model
	}
}

// WithLanguage sets the language code for recognition (e.g., "en", "de").
func WithLanguage(language string) Option {
	return func(p *Provider) {
		p.language = language
	}
}

// WithSampleRate sets the audio sample rate in Hz for the provider-level default.
func WithSampleRate(rate int) Option {
	return func(p *Provider) {
		p.sampleRate = rate
	}
}

// WithBaseURL overrides the default WebSocket endpoint. Intended for testing.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) {
		p.baseURL = baseURL
	}
}

// Provider implements stt.Provider backed by the ElevenLabs Scribe v2 Realtime
// streaming API.
type Provider struct {
	apiKey     string
	model      string
	language   string
	sampleRate int
	baseURL    string
}

var _ stt.Provider = (*Provider)(nil)

// New creates a new ElevenLabs STT Provider. apiKey must be non-empty.
func New(apiKey string, opts ...Option) (*Provider, error) {
	if apiKey == "" {
		return nil, errors.New("elevenlabs: apiKey must not be empty")
	}
	p := &Provider{
		apiKey:     apiKey,
		model:      defaultModel,
		language:   defaultLanguage,
		sampleRate: defaultSampleRate,
		baseURL:    defaultBaseURL,
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// StartStream opens a streaming transcription session with ElevenLabs.
// It respects cfg.SampleRate and cfg.Language.
func (p *Provider) StartStream(ctx context.Context, cfg stt.StreamConfig) (stt.SessionHandle, error) {
	wsURL, err := p.buildURL(cfg)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: build URL: %w", err)
	}

	headers := http.Header{}
	headers.Set("xi-api-key", p.apiKey)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: dial: %w", err)
	}
	conn.SetReadLimit(1 << 20) // 1 MiB

	sr := cfg.SampleRate
	if sr == 0 {
		sr = p.sampleRate
	}

	sess := &session{
		conn:       conn,
		partials:   make(chan stt.Transcript, 64),
		finals:     make(chan stt.Transcript, 64),
		audio:      make(chan []byte, 256),
		sampleRate: sr,
		done:       make(chan struct{}),
		writeDone:  make(chan struct{}),
	}

	sess.wg.Add(2)
	go sess.readLoop(ctx)
	go sess.writeLoop(ctx)

	return sess, nil
}

// buildURL constructs the ElevenLabs streaming endpoint URL for the given config.
func (p *Provider) buildURL(cfg stt.StreamConfig) (string, error) {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return "", err
	}

	lang := cfg.Language
	if lang == "" {
		lang = p.language
	}
	// Normalise BCP-47 (e.g. "en-US") to ISO 639-1 ("en") for ElevenLabs.
	if idx := strings.IndexByte(lang, '-'); idx > 0 {
		lang = lang[:idx]
	}

	sr := cfg.SampleRate
	if sr == 0 {
		sr = p.sampleRate
	}

	q := u.Query()
	q.Set("model_id", p.model)
	q.Set("language_code", lang)
	q.Set("audio_format", "pcm_"+strconv.Itoa(sr))
	q.Set("include_timestamps", "true")
	q.Set("commit_strategy", "manual")

	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ---- JSON message types ----

// audioChunkMessage is the client-to-server message for sending audio data.
type audioChunkMessage struct {
	MessageType string `json:"message_type"`
	AudioBase64 string `json:"audio_base_64"`
	Commit      bool   `json:"commit,omitempty"`
	SampleRate  int    `json:"sample_rate"`
}

// serverMessage is the envelope used to dispatch on message_type.
type serverMessage struct {
	MessageType string `json:"message_type"`
}

// partialTranscriptMessage is the partial_transcript event from the server.
type partialTranscriptMessage struct {
	MessageType string `json:"message_type"`
	Text        string `json:"text"`
}

// committedTranscriptMessage covers both committed_transcript and
// committed_transcript_with_timestamps events.
type committedTranscriptMessage struct {
	MessageType  string          `json:"message_type"`
	Text         string          `json:"text"`
	LanguageCode string          `json:"language_code"`
	Words        []wordTimestamp `json:"words"`
}

// wordTimestamp is a single word entry in a committed transcript with timestamps.
type wordTimestamp struct {
	Text      string  `json:"text"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Type      string  `json:"type"`
	SpeakerID string  `json:"speaker_id"`
	LogProb   float64 `json:"logprob"`
}

// errorMessage is an error event from the server.
type errorMessage struct {
	MessageType string `json:"message_type"`
	Error       string `json:"error"`
}

// fatalErrors are server error message_types that should terminate the session.
var fatalErrors = map[string]bool{
	"auth_error":           true,
	"invalid_api_key":      true,
	"quota_exceeded":       true,
	"invalid_audio_format": true,
}

// ---- session ----

// session is a live ElevenLabs streaming session. It implements stt.SessionHandle.
type session struct {
	conn     *websocket.Conn
	partials chan stt.Transcript
	finals   chan stt.Transcript
	audio    chan []byte

	sampleRate int
	done       chan struct{}
	writeDone  chan struct{} // closed when writeLoop exits
	once       sync.Once
	wg         sync.WaitGroup
}

var _ stt.SessionHandle = (*session)(nil)

// SendAudio queues a PCM audio chunk for delivery to ElevenLabs.
func (s *session) SendAudio(chunk []byte) error {
	select {
	case <-s.done:
		return errors.New("elevenlabs: session is closed")
	default:
	}
	select {
	case s.audio <- chunk:
		return nil
	case <-s.done:
		return errors.New("elevenlabs: session is closed")
	}
}

// Partials returns the channel of interim transcripts.
func (s *session) Partials() <-chan stt.Transcript { return s.partials }

// Finals returns the channel of final transcripts.
func (s *session) Finals() <-chan stt.Transcript { return s.finals }

// SetKeywords is not supported by ElevenLabs Scribe v2 and returns
// stt.ErrNotSupported.
func (s *session) SetKeywords(_ []stt.KeywordBoost) error {
	return fmt.Errorf("elevenlabs: %w", stt.ErrNotSupported)
}

// Close terminates the session cleanly. It signals writeLoop to drain
// remaining audio and send a final commit, then closes the WebSocket so
// readLoop's conn.Read unblocks and the goroutine exits promptly.
func (s *session) Close() error {
	s.once.Do(func() {
		close(s.done)

		// Wait for writeLoop to drain remaining audio and send the final
		// commit. This is normally fast (sub-millisecond for in-process work
		// plus one network write), but we cap the wait to avoid hanging on a
		// broken connection.
		select {
		case <-s.writeDone:
		case <-time.After(closeTimeout):
			slog.Warn("elevenlabs: timed out waiting for write loop commit")
		}

		// Close the WebSocket. The clean close handshake lets readLoop
		// receive any pending committed_transcript before the peer
		// acknowledges the close frame. Once the handshake completes (or
		// the library's internal timeout fires), conn.Read returns an error
		// and readLoop exits.
		s.conn.Close(websocket.StatusNormalClosure, "session closed")

		s.wg.Wait()
	})
	return nil
}

// writeLoop reads from the audio channel, base64-encodes PCM chunks, and sends
// them as JSON messages to ElevenLabs. On shutdown it drains remaining audio
// and sends a commit message.
func (s *session) writeLoop(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.writeDone)
	for {
		select {
		case chunk, ok := <-s.audio:
			if !ok {
				return
			}
			if err := s.sendAudioChunk(ctx, chunk, false); err != nil {
				return
			}
		case <-s.done:
			// Drain remaining audio.
			for {
				select {
				case chunk, ok := <-s.audio:
					if !ok {
						return
					}
					_ = s.sendAudioChunk(ctx, chunk, false)
				default:
					// Send commit as the final action.
					_ = s.sendAudioChunk(ctx, nil, true)
					return
				}
			}
		}
	}
}

// sendAudioChunk encodes and sends a single audio chunk message.
func (s *session) sendAudioChunk(ctx context.Context, chunk []byte, commit bool) error {
	msg := audioChunkMessage{
		MessageType: "input_audio_chunk",
		AudioBase64: base64.StdEncoding.EncodeToString(chunk),
		Commit:      commit,
		SampleRate:  s.sampleRate,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("elevenlabs: marshal audio chunk: %w", err)
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}

// readLoop receives JSON messages from ElevenLabs and dispatches them to the
// partials and finals channels.
func (s *session) readLoop(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.partials)
	defer close(s.finals)

	for {
		_, msg, err := s.conn.Read(ctx)
		if err != nil {
			return
		}

		t, ok := parseResponse(msg)
		if !ok {
			continue
		}

		if t.IsFinal {
			select {
			case s.finals <- t:
			case <-s.done:
				// During shutdown the committed transcript from a manual
				// commit may still arrive. Try a non-blocking send so the
				// result is not silently dropped when buffer space exists.
				select {
				case s.finals <- t:
				default:
				}
			}
		} else {
			select {
			case s.partials <- t:
			case <-s.done:
			}
		}
	}
}

// parseResponse parses a raw ElevenLabs WebSocket message into a Transcript.
// Returns (Transcript, true) for partial and committed transcripts, or
// (zero, false) if the message should be ignored.
func parseResponse(data []byte) (stt.Transcript, bool) {
	var env serverMessage
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Debug("elevenlabs: failed to parse response", "err", err)
		return stt.Transcript{}, false
	}

	switch env.MessageType {
	case "partial_transcript":
		var msg partialTranscriptMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Debug("elevenlabs: failed to parse partial_transcript", "err", err)
			return stt.Transcript{}, false
		}
		return stt.Transcript{
			Text: msg.Text,
		}, true

	case "committed_transcript", "committed_transcript_with_timestamps":
		var msg committedTranscriptMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Debug("elevenlabs: failed to parse committed_transcript", "err", err)
			return stt.Transcript{}, false
		}

		words := make([]stt.WordDetail, 0, len(msg.Words))
		for _, w := range msg.Words {
			if w.Type != "word" {
				continue
			}
			words = append(words, stt.WordDetail{
				Word:       w.Text,
				Start:      time.Duration(w.Start * float64(time.Second)),
				End:        time.Duration(w.End * float64(time.Second)),
				Confidence: math.Exp(w.LogProb),
			})
		}

		return stt.Transcript{
			Text:    msg.Text,
			IsFinal: true,
			Words:   words,
		}, true

	case "session_started":
		slog.Debug("elevenlabs: session started")
		return stt.Transcript{}, false

	default:
		if fatalErrors[env.MessageType] {
			var em errorMessage
			_ = json.Unmarshal(data, &em)
			slog.Error("elevenlabs: fatal server error", "type", env.MessageType, "error", em.Error)
			return stt.Transcript{}, false
		}
		// Transient errors and unknown message types.
		if strings.Contains(env.MessageType, "error") {
			var em errorMessage
			_ = json.Unmarshal(data, &em)
			slog.Warn("elevenlabs: server error", "type", env.MessageType, "error", em.Error)
		} else {
			slog.Debug("elevenlabs: unknown message type", "type", env.MessageType)
		}
		return stt.Transcript{}, false
	}
}
