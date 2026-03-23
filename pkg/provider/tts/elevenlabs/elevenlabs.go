// Package elevenlabs provides an ElevenLabs-backed TTS provider using the
// ElevenLabs streaming WebSocket API. It implements the tts.Provider interface.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	"github.com/coder/websocket"
)

const (
	wsEndpointFmt    = "wss://api.elevenlabs.io/v1/text-to-speech/%s/stream-input?model_id=%s"
	voicesEndpoint   = "https://api.elevenlabs.io/v1/voices"
	addVoiceEndpoint = "https://api.elevenlabs.io/v1/voices/add"
	defaultModel     = "eleven_flash_v2_5"
	defaultOutputFmt = "pcm_16000"
	defaultPoolSize  = 1
	defaultMaxIdle   = 30 * time.Second
	dialAheadTimeout = 10 * time.Second
)

// Option is a functional option for configuring the ElevenLabs Provider.
type Option func(*Provider)

// WithModel sets the ElevenLabs model ID (e.g., "eleven_flash_v2_5").
func WithModel(model string) Option {
	return func(p *Provider) {
		p.model = model
	}
}

// WithOutputFormat sets the audio output format (e.g., "pcm_16000", "pcm_24000").
func WithOutputFormat(format string) Option {
	return func(p *Provider) {
		p.outputFormat = format
	}
}

// WithPoolSize sets the maximum number of pre-warmed WebSocket connections to
// maintain per voice ID. Default is 1. Setting to 0 disables pooling.
func WithPoolSize(n int) Option {
	return func(p *Provider) {
		p.poolSize = n
	}
}

// WithMaxIdleTime sets the maximum time a pre-warmed connection can sit idle
// in the pool before being evicted. Default is 30 seconds.
func WithMaxIdleTime(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.maxIdle = d
		}
	}
}

// warmConn is a pre-dialed WebSocket connection waiting in the pool.
type warmConn struct {
	conn     *websocket.Conn
	dialedAt time.Time
}

// Provider implements tts.Provider backed by the ElevenLabs streaming API.
//
// Provider maintains a pool of pre-dialed WebSocket connections to reduce
// per-synthesis dial latency. After each SynthesizeStream call, a replacement
// connection is dialed in the background so the next call can skip the
// TCP+TLS+WebSocket handshake.
type Provider struct {
	apiKey       string
	model        string
	outputFormat string
	httpClient   *http.Client
	// addVoiceURL is the endpoint for POST /v1/voices/add.
	// It defaults to addVoiceEndpoint and can be overridden in tests.
	addVoiceURL string

	// dialFunc overrides websocket.Dial for testing. nil means use the default.
	dialFunc func(ctx context.Context, url string) (*websocket.Conn, error)

	// Pool configuration.
	poolSize int           // max pre-warmed connections per voice (default: 1)
	maxIdle  time.Duration // max time a connection can sit idle before eviction

	// Pool state — protected by mu.
	mu     sync.Mutex
	pool   map[string][]*warmConn // voiceID → stack of pre-dialed connections
	closed bool
	wg     sync.WaitGroup // tracks in-flight dial-ahead goroutines
}

// Compile-time interface assertions.
var _ tts.Warmer = (*Provider)(nil)

// New creates a new ElevenLabs Provider. apiKey must be non-empty.
func New(apiKey string, opts ...Option) (*Provider, error) {
	if apiKey == "" {
		return nil, errors.New("elevenlabs: apiKey must not be empty")
	}
	p := &Provider{
		apiKey:       apiKey,
		model:        defaultModel,
		outputFormat: defaultOutputFmt,
		httpClient:   &http.Client{},
		addVoiceURL:  addVoiceEndpoint,
		poolSize:     defaultPoolSize,
		maxIdle:      defaultMaxIdle,
		pool:         make(map[string][]*warmConn),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// ---- WebSocket message types ----

// textMessage is the JSON payload sent to ElevenLabs for each text fragment.
type textMessage struct {
	Text          string         `json:"text"`
	VoiceSettings *voiceSettings `json:"voice_settings,omitempty"`
}

// voiceSettings mirrors the ElevenLabs voice_settings object.
type voiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
}

// audioResponse is the JSON message received from ElevenLabs over the WebSocket.
type audioResponse struct {
	Audio   string `json:"audio"` // base64-encoded PCM
	IsFinal bool   `json:"isFinal"`
	Message string `json:"message,omitempty"` // error or info
}

// boiMessage is used for the initial "begin of input" handshake.
type boiMessage struct {
	Text          string         `json:"text"`
	VoiceSettings *voiceSettings `json:"voice_settings,omitempty"`
	XiAPIKey      string         `json:"xi_api_key"`
	OutputFormat  string         `json:"output_format,omitempty"`
}

// SynthesizeStream opens a WebSocket to ElevenLabs, pipes text fragments from
// the text channel, and returns a channel emitting raw PCM audio chunks.
//
// If a pre-warmed connection is available for the requested voice it is used
// instead of dialing a new one, saving 50-150 ms of TCP+TLS+WS handshake
// latency. After acquiring a connection a replacement is dialed in the
// background so the next call for the same voice is also fast.
//
// The returned audio channel is closed when synthesis is complete or ctx is cancelled.
func (p *Provider) SynthesizeStream(ctx context.Context, text <-chan string, voice tts.VoiceProfile) (<-chan []byte, error) {
	if voice.ID == "" {
		return nil, errors.New("elevenlabs: voice.ID must not be empty")
	}

	conn, err := p.acquireAndInit(ctx, voice.ID)
	if err != nil {
		return nil, err
	}

	// Pre-dial a replacement connection in the background for the next call.
	p.dialAhead(voice.ID)

	audioCh := make(chan []byte, 256)

	slog.Debug("elevenlabs: stream started", "voiceID", voice.ID, "model", p.model)

	go func() {
		defer close(audioCh)
		defer conn.Close(websocket.StatusNormalClosure, "done")

		// Start reader goroutine.
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			chunks := 0
			totalBytes := 0
			for {
				_, msg, err := conn.Read(ctx)
				if err != nil {
					slog.Debug("elevenlabs: reader done", "chunks", chunks, "totalBytes", totalBytes, "error", err)
					return
				}
				var resp audioResponse
				if err := json.Unmarshal(msg, &resp); err != nil {
					slog.Debug("elevenlabs: unmarshal error", "error", err)
					continue
				}
				if resp.IsFinal {
					slog.Debug("elevenlabs: received isFinal marker", "chunks", chunks, "totalBytes", totalBytes)
				}
				if resp.Message != "" {
					slog.Debug("elevenlabs: server message", "message", resp.Message)
				}
				if resp.Audio == "" {
					continue
				}
				pcm, err := base64.StdEncoding.DecodeString(resp.Audio)
				if err != nil {
					slog.Debug("elevenlabs: base64 decode error", "error", err)
					continue
				}
				chunks++
				totalBytes += len(pcm)
				select {
				case audioCh <- pcm:
				case <-ctx.Done():
					slog.Debug("elevenlabs: reader cancelled by context", "chunks", chunks)
					return
				}
			}
		}()

		// Write text fragments to ElevenLabs.
		vs := &voiceSettings{Stability: 0.5, SimilarityBoost: 0.75}
		for {
			select {
			case sentence, ok := <-text:
				if !ok {
					slog.Debug("elevenlabs: text channel closed, sending flush")
					// Text channel closed — send flush command.
					flush := textMessage{Text: ""}
					flushBytes, _ := json.Marshal(flush)
					_ = conn.Write(ctx, websocket.MessageText, flushBytes)
					// Wait for the reader to finish draining audio.
					<-readDone
					slog.Debug("elevenlabs: reader drained, stream complete")
					return
				}
				if sentence == "" {
					continue
				}
				slog.Debug("elevenlabs: sending text fragment", "len", len(sentence))
				payload := textMessage{Text: sentence, VoiceSettings: vs}
				// Only send voice settings on the first chunk; subsequent chunks can omit them.
				vs = nil
				msgBytes, _ := json.Marshal(payload)
				if err := conn.Write(ctx, websocket.MessageText, msgBytes); err != nil {
					slog.Debug("elevenlabs: write error", "error", err)
					return
				}
			case <-ctx.Done():
				slog.Debug("elevenlabs: writer cancelled by context")
				return
			}
		}
	}()

	return audioCh, nil
}

// ---- Connection pool ----

// dialWS dials a new WebSocket connection for the given voice ID.
func (p *Provider) dialWS(ctx context.Context, voiceID string) (*websocket.Conn, error) {
	wsURL := fmt.Sprintf(wsEndpointFmt, voiceID, p.model)
	if p.dialFunc != nil {
		return p.dialFunc(ctx, wsURL)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	return conn, err
}

// acquireAndInit returns a WebSocket connection with the BOI message already
// sent, ready for text streaming. It first tries a pre-warmed connection from
// the pool; if the warm connection is stale (BOI write fails), it falls back
// to a fresh dial.
func (p *Provider) acquireAndInit(ctx context.Context, voiceID string) (*websocket.Conn, error) {
	boi := boiMessage{
		Text: " ", // ElevenLabs requires a non-empty first text value
		VoiceSettings: &voiceSettings{
			Stability:       0.5,
			SimilarityBoost: 0.75,
		},
		XiAPIKey:     p.apiKey,
		OutputFormat: p.outputFormat,
	}
	boiBytes, _ := json.Marshal(boi)

	// Try a pre-warmed connection first.
	if conn := p.takeWarm(voiceID); conn != nil {
		conn.SetReadLimit(1 << 20) // 1 MiB
		if err := conn.Write(ctx, websocket.MessageText, boiBytes); err == nil {
			slog.Debug("elevenlabs: using pre-warmed connection", "voiceID", voiceID)
			return conn, nil
		}
		// Warm connection went stale — close and fall through to fresh dial.
		conn.Close(websocket.StatusInternalError, "stale")
		slog.Debug("elevenlabs: warm connection stale, redialing", "voiceID", voiceID)
	}

	// No warm connection or it was stale — dial fresh.
	conn, err := p.dialWS(ctx, voiceID)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: dial: %w", err)
	}
	conn.SetReadLimit(1 << 20) // 1 MiB
	if err := conn.Write(ctx, websocket.MessageText, boiBytes); err != nil {
		conn.Close(websocket.StatusInternalError, "failed to send BOI")
		return nil, fmt.Errorf("elevenlabs: send BOI: %w", err)
	}
	return conn, nil
}

// takeWarm removes and returns a fresh pre-warmed connection for voiceID, or
// nil if none is available. Stale connections (older than maxIdle) are closed
// and discarded.
func (p *Provider) takeWarm(voiceID string) *websocket.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || p.pool == nil {
		return nil
	}

	conns := p.pool[voiceID]
	for len(conns) > 0 {
		// Pop from the end (LIFO — most recently dialed is freshest).
		wc := conns[len(conns)-1]
		conns = conns[:len(conns)-1]
		p.pool[voiceID] = conns

		if time.Since(wc.dialedAt) < p.maxIdle {
			return wc.conn
		}
		// Stale — close it.
		wc.conn.Close(websocket.StatusNormalClosure, "idle timeout")
		slog.Debug("elevenlabs: evicted stale connection", "voiceID", voiceID,
			"age", time.Since(wc.dialedAt).Round(time.Millisecond))
	}
	return nil
}

// putWarm stores a pre-dialed connection in the pool. Returns false (and does
// NOT close the connection) if the pool is full or the provider is closed.
func (p *Provider) putWarm(voiceID string, conn *websocket.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return false
	}
	if p.pool == nil {
		p.pool = make(map[string][]*warmConn)
	}

	conns := p.pool[voiceID]
	if len(conns) >= p.poolSize {
		return false
	}
	p.pool[voiceID] = append(conns, &warmConn{
		conn:     conn,
		dialedAt: time.Now(),
	})
	return true
}

// dialAhead starts a background goroutine that dials a replacement WebSocket
// connection for voiceID and stores it in the pool.
func (p *Provider) dialAhead(voiceID string) {
	if p.poolSize <= 0 {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), dialAheadTimeout)
		defer cancel()

		conn, err := p.dialWS(ctx, voiceID)
		if err != nil {
			slog.Debug("elevenlabs: dial-ahead failed", "voiceID", voiceID, "error", err)
			return
		}

		if !p.putWarm(voiceID, conn) {
			conn.Close(websocket.StatusNormalClosure, "pool full or closed")
			return
		}
		slog.Debug("elevenlabs: dial-ahead connection ready", "voiceID", voiceID)
	}()
}

// Warm pre-dials WebSocket connections for the given voices so that subsequent
// SynthesizeStream calls can skip the dial latency.
func (p *Provider) Warm(ctx context.Context, voices ...tts.VoiceProfile) error {
	for _, v := range voices {
		if v.ID == "" {
			continue
		}
		conn, err := p.dialWS(ctx, v.ID)
		if err != nil {
			return fmt.Errorf("elevenlabs: warm %s: %w", v.ID, err)
		}
		if !p.putWarm(v.ID, conn) {
			conn.Close(websocket.StatusNormalClosure, "pool full or closed")
		}
	}
	return nil
}

// Close releases all pre-warmed connections and waits for in-flight dial-ahead
// goroutines to finish. Safe to call multiple times.
func (p *Provider) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	for voiceID, conns := range p.pool {
		for _, wc := range conns {
			wc.conn.Close(websocket.StatusNormalClosure, "provider closed")
		}
		delete(p.pool, voiceID)
	}
	p.mu.Unlock()

	p.wg.Wait()
	return nil
}

// ---- ListVoices ----

// voicesResponse is the top-level response from GET /v1/voices.
type voicesResponse struct {
	Voices []elevenLabsVoice `json:"voices"`
}

// elevenLabsVoice is a single voice entry from the ElevenLabs API.
type elevenLabsVoice struct {
	VoiceID  string            `json:"voice_id"`
	Name     string            `json:"name"`
	Category string            `json:"category"`
	Labels   map[string]string `json:"labels"`
}

// ListVoices returns all voices available from ElevenLabs for the configured API key.
func (p *Provider) ListVoices(ctx context.Context) ([]tts.VoiceProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, voicesEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: list voices: %w", err)
	}
	req.Header.Set("xi-api-key", p.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: list voices HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs: list voices: unexpected status %d", resp.StatusCode)
	}

	var vr voicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, fmt.Errorf("elevenlabs: list voices decode: %w", err)
	}

	profiles := make([]tts.VoiceProfile, 0, len(vr.Voices))
	for _, v := range vr.Voices {
		meta := make(map[string]string, len(v.Labels)+1)
		maps.Copy(meta, v.Labels)
		if v.Category != "" {
			meta["category"] = v.Category
		}
		profiles = append(profiles, tts.VoiceProfile{
			ID:       v.VoiceID,
			Name:     v.Name,
			Provider: "elevenlabs",
			Metadata: meta,
		})
	}
	return profiles, nil
}

// ---- CloneVoice ----

// CloneVoiceOption is a functional option for CloneVoiceWithOptions.
type CloneVoiceOption func(*cloneVoiceConfig)

// cloneVoiceConfig holds configuration for a voice cloning request.
type cloneVoiceConfig struct {
	name                  string
	removeBackgroundNoise bool
	description           string
	labels                map[string]string
}

// WithCloneName overrides the auto-generated voice name used when cloning.
func WithCloneName(name string) CloneVoiceOption {
	return func(c *cloneVoiceConfig) {
		c.name = name
	}
}

// WithRemoveBackgroundNoise enables or disables background noise removal during cloning.
func WithRemoveBackgroundNoise(enabled bool) CloneVoiceOption {
	return func(c *cloneVoiceConfig) {
		c.removeBackgroundNoise = enabled
	}
}

// WithDescription sets an optional human-readable description for the cloned voice.
func WithDescription(desc string) CloneVoiceOption {
	return func(c *cloneVoiceConfig) {
		c.description = desc
	}
}

// WithLabels sets optional metadata labels (e.g., language, accent) for the cloned voice.
func WithLabels(labels map[string]string) CloneVoiceOption {
	return func(c *cloneVoiceConfig) {
		c.labels = labels
	}
}

// addVoiceResponse is the JSON response body from POST /v1/voices/add.
type addVoiceResponse struct {
	VoiceID              string `json:"voice_id"`
	RequiresVerification bool   `json:"requires_verification"`
}

// CloneVoice clones a voice from the provided audio samples using the ElevenLabs
// /v1/voices/add API. It delegates to CloneVoiceWithOptions with no options.
//
// samples must be non-nil and non-empty; each element is treated as a WAV audio file.
// Returns a *tts.VoiceProfile with the provider-assigned voice ID on success.
func (p *Provider) CloneVoice(ctx context.Context, samples [][]byte) (*tts.VoiceProfile, error) {
	return p.CloneVoiceWithOptions(ctx, samples)
}

// CloneVoiceWithOptions clones a voice from audio samples with optional configuration.
//
// samples must be non-nil and non-empty. Each sample is uploaded as a separate WAV
// file part (named sample_0.wav, sample_1.wav, …). The voice name defaults to
// "glyphoxa-clone-<unix-timestamp>" but can be overridden with WithCloneName.
//
// Returns a *tts.VoiceProfile with the provider-assigned voice ID on success.
func (p *Provider) CloneVoiceWithOptions(ctx context.Context, samples [][]byte, opts ...CloneVoiceOption) (*tts.VoiceProfile, error) {
	if samples == nil {
		return nil, errors.New("elevenlabs: samples must not be nil")
	}
	if len(samples) == 0 {
		return nil, errors.New("elevenlabs: samples must not be empty")
	}

	cfg := &cloneVoiceConfig{
		name: fmt.Sprintf("glyphoxa-clone-%d", time.Now().Unix()),
	}
	for _, o := range opts {
		o(cfg)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Required: voice name.
	if err := mw.WriteField("name", cfg.name); err != nil {
		return nil, fmt.Errorf("elevenlabs: clone voice: write name field: %w", err)
	}

	// Required: audio sample files.
	for i, sample := range samples {
		fw, err := mw.CreateFormFile("files", fmt.Sprintf("sample_%d.wav", i))
		if err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: create file part %d: %w", i, err)
		}
		if _, err := fw.Write(sample); err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: write file part %d: %w", i, err)
		}
	}

	// Optional: remove background noise.
	if cfg.removeBackgroundNoise {
		if err := mw.WriteField("remove_background_noise", "true"); err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: write remove_background_noise: %w", err)
		}
	}

	// Optional: description.
	if cfg.description != "" {
		if err := mw.WriteField("description", cfg.description); err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: write description: %w", err)
		}
	}

	// Optional: labels (JSON-encoded map).
	if len(cfg.labels) > 0 {
		labelsJSON, err := json.Marshal(cfg.labels)
		if err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: marshal labels: %w", err)
		}
		if err := mw.WriteField("labels", string(labelsJSON)); err != nil {
			return nil, fmt.Errorf("elevenlabs: clone voice: write labels: %w", err)
		}
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("elevenlabs: clone voice: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.addVoiceURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: clone voice: create request: %w", err)
	}
	req.Header.Set("xi-api-key", p.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: clone voice: HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs: clone voice: unexpected status %d", resp.StatusCode)
	}

	var avr addVoiceResponse
	if err := json.NewDecoder(resp.Body).Decode(&avr); err != nil {
		return nil, fmt.Errorf("elevenlabs: clone voice: decode response: %w", err)
	}

	return &tts.VoiceProfile{
		ID:       avr.VoiceID,
		Name:     cfg.name,
		Provider: "elevenlabs",
		Metadata: map[string]string{},
	}, nil
}

// ---- helpers ----

// buildWSMessage constructs the JSON text payload for a single text fragment.
// Used by tests to verify the payload shape without opening a real connection.
func buildWSMessage(text string, vs *voiceSettings) ([]byte, error) {
	return json.Marshal(textMessage{Text: text, VoiceSettings: vs})
}

// buildURLForVoice constructs the WebSocket URL for a given voice and model.
func buildURLForVoice(voiceID, model string) string {
	return fmt.Sprintf(wsEndpointFmt, voiceID, model)
}

// parseVoicesResponse parses a raw JSON byte slice (matching the ElevenLabs
// /v1/voices response) into a slice of VoiceProfile values.
func parseVoicesResponse(data []byte) ([]tts.VoiceProfile, error) {
	var vr voicesResponse
	if err := json.Unmarshal(data, &vr); err != nil {
		return nil, err
	}
	profiles := make([]tts.VoiceProfile, 0, len(vr.Voices))
	for _, v := range vr.Voices {
		meta := make(map[string]string, len(v.Labels)+1)
		maps.Copy(meta, v.Labels)
		if v.Category != "" {
			meta["category"] = v.Category
		}
		profiles = append(profiles, tts.VoiceProfile{
			ID:       v.VoiceID,
			Name:     v.Name,
			Provider: "elevenlabs",
			Metadata: meta,
		})
	}
	return profiles, nil
}
