package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// streamReadBuffer is the size of one Read call against the streaming
// response body. 4 KiB ≈ 85 ms of 24 kHz mono int16 PCM — small enough to
// keep first-frame latency low, large enough to amortize syscall cost.
const streamReadBuffer = 4096

// synthesizeBody mirrors the ElevenLabs POST /v1/text-to-speech body.
type synthesizeBody struct {
	Text                            string                           `json:"text"`
	ModelID                         string                           `json:"model_id,omitempty"`
	LanguageCode                    string                           `json:"language_code,omitempty"`
	VoiceSettings                   *VoiceSettings                   `json:"voice_settings,omitempty"`
	Seed                            *int64                           `json:"seed,omitempty"`
	PronunciationDictionaryLocators []PronunciationDictionaryLocator `json:"pronunciation_dictionary_locators,omitempty"`
}

// Synthesize implements [tts.Synthesizer]. Sentence text is forwarded
// verbatim — ElevenLabs v3 inline `[bracket]` audio tags survive the round
// trip because the API treats them as part of the prompt.
//
// Streaming model:
//   - HTTP body is read in [streamReadBuffer]-sized windows and emitted as
//     [tts.AudioChunk]s with the sample rate parsed from the request's
//     output_format and Channels=1 (ElevenLabs PCM is mono).
//   - The returned channel closes on EOF (synthesis complete), ctx
//     cancellation (e.g. barge-in per ADR-0022), or the first stream read
//     error (mid-stream failure).
//   - The function returns a non-nil error only when the call cannot be
//     started (missing key, bad request, non-2xx response). A mid-stream read
//     failure under a live ctx emits a terminal [tts.AudioChunk] with Err set
//     before the close (#436), so the dispatch layer never commits the
//     truncated sentence as fully delivered; a ctx cancellation closes with no
//     terminal chunk (the cut was the caller's).
func (c *Client) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs.Synthesize: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if req.Voice.VoiceID == "" {
		return nil, fmt.Errorf("elevenlabs.Synthesize: SynthesizeRequest.Voice.VoiceID is empty")
	}

	settings, err := mergeSettings(req.Voice.Settings, req.OverrideSettings)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.Synthesize: %w", err)
	}
	if settings.ModelID == "" {
		settings.ModelID = ModelV3
	}
	if settings.OutputFormat == "" {
		settings.OutputFormat = DefaultOutputFormat
	}
	sr := sampleRateFromOutputFormat(settings.OutputFormat)
	if sr == 0 {
		return nil, fmt.Errorf("elevenlabs.Synthesize: output_format %q is not PCM (orchestrator requires PCM)", settings.OutputFormat)
	}

	body := synthesizeBody{
		Text:                            req.Sentence,
		ModelID:                         settings.ModelID,
		LanguageCode:                    settings.LanguageCode,
		VoiceSettings:                   settings.VoiceSettings,
		Seed:                            settings.Seed,
		PronunciationDictionaryLocators: settings.PronunciationDictionaryLocators,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.Synthesize: marshal body: %w", err)
	}

	u := fmt.Sprintf("%s/v1/text-to-speech/%s/stream?output_format=%s",
		strings.TrimRight(c.baseURL, "/"),
		url.PathEscape(req.Voice.VoiceID),
		url.QueryEscape(settings.OutputFormat),
	)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.Synthesize: build request: %w", err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/*")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.Synthesize: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readErrorResponse(resp, "Synthesize")
	}

	ch := make(chan tts.AudioChunk)
	go streamPCM(ctx, resp.Body, ch, sr)
	return ch, nil
}

// streamPCM reads PCM bytes from r in [streamReadBuffer] windows, emits them
// on ch, and closes ch on EOF / ctx cancel / read error. r is closed before
// return so the underlying connection is released to the http.Client's pool.
func streamPCM(ctx context.Context, r io.ReadCloser, ch chan<- tts.AudioChunk, sampleRate int) {
	defer close(ch)
	defer r.Close()

	buf := make([]byte, streamReadBuffer)
	carried := false // buf[0] holds the low byte of a sample split across reads
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// Reserve buf[0] for a byte carried over from the previous read so a
		// sample straddling a read boundary stays aligned (each int16 PCM sample
		// is two bytes; downstream [audio.FromPCM16LE] requires an even count).
		off := 0
		if carried {
			off = 1
		}
		n, err := r.Read(buf[off:])
		total := off + n
		if total > 0 {
			// Emit a whole number of samples; hold any trailing odd byte for the
			// next read instead of dropping it (dropping would shift — and so
			// corrupt — every subsequent sample).
			emit := total &^ 1
			if emit > 0 {
				chunk := make([]byte, emit)
				copy(chunk, buf[:emit])
				select {
				case <-ctx.Done():
					return
				case ch <- tts.AudioChunk{PCM: chunk, SampleRate: sampleRate, Channels: 1}:
				}
			}
			// Carry the leftover low byte to the front for the next read. Done
			// after the copy above so the emitted chunk keeps its first byte.
			if emit < total {
				buf[0] = buf[emit]
				carried = true
			} else {
				carried = false
			}
		}
		if err != nil {
			// io.EOF is normal completion: close with no terminal chunk. Any other
			// read error under a live ctx is a MID-STREAM failure — report it as a
			// terminal Err chunk before the close (#436) so the dispatch layer can
			// tell the truncated sentence from a complete one. A cancelled ctx
			// closes silently (barge-in — the caller cut the stream); the send is
			// ctx-guarded so a torn-down drain never wedges this goroutine. A final
			// dangling byte (a truncated last sample) is intentionally dropped.
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				select {
				case ch <- tts.AudioChunk{Err: err}:
				case <-ctx.Done():
				}
			}
			return
		}
	}
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and returns it as a typed [*providererr.HTTPError] so the
// retry helper can classify the call by status code via errors.As (#124). Its
// Error() text is byte-identical to the former fmt.Errorf literal, so grep-based
// harnesses and logs are unchanged.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &providererr.HTTPError{
		Op:         "elevenlabs." + op,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(snippet)),
	}
}
