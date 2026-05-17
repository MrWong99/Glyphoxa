package elevenlabs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// Model is the canonical ElevenLabs Scribe v2 model identifier — the v1.0
// successor to scribe_v1 and the model the adapter targets unconditionally.
const Model = "scribe_v2"

// FileFormatPCMS16LE16 is the ElevenLabs `file_format` value that accepts a
// raw little-endian signed-16-bit PCM stream at 16 kHz mono — matches
// [audio.Frame] natively, so no WAV header / re-encoding is needed.
const FileFormatPCMS16LE16 = "pcm_s16le_16"

// Transcribe implements [stt.Recognizer]. One call → one POST
// /v1/speech-to-text request → one [stt.Transcript].
//
// Frames are streamed verbatim as `file_format=pcm_s16le_16`: the underlying
// little-endian int16 samples are concatenated in frame order and uploaded
// as the multipart `file` part. No vendor-side resampling is invoked because
// the orchestrator's STT stage already feeds frames at the rate the
// recognizer requires.
//
// Returns a non-nil error when the call cannot be started (missing key) or
// when the response is non-2xx; on success returns the response's `text`
// field verbatim.
func (c *Client) Transcribe(ctx context.Context, frames []audio.Frame) (stt.Transcript, error) {
	if c.apiKey == "" {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("model_id", Model); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write model_id: %w", err)
	}
	if err := mw.WriteField("file_format", FileFormatPCMS16LE16); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write file_format: %w", err)
	}
	filePart, err := mw.CreateFormFile("file", "utterance.pcm")
	if err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: create file part: %w", err)
	}
	if err := writePCM16LE(filePart, frames); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write PCM: %w", err)
	}
	if err := mw.Close(); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: close multipart: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/speech-to-text"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: build request: %w", err)
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stt.Transcript{}, readErrorResponse(resp, "Transcribe")
	}

	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: decode body: %w", err)
	}
	return stt.Transcript{Text: decoded.Text}, nil
}

// writePCM16LE emits the concatenated little-endian int16 sample stream of
// frames to w in frame order. Buffered scratch keeps the encoding loop free
// of per-sample allocations.
func writePCM16LE(w io.Writer, frames []audio.Frame) error {
	var buf [2]byte
	for _, f := range frames {
		for _, s := range f.Samples() {
			binary.LittleEndian.PutUint16(buf[:], uint16(s))
			if _, err := w.Write(buf[:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and wraps it as an error naming the operation.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("elevenlabs.%s: HTTP %d %s: %s",
		op, resp.StatusCode, resp.Status, strings.TrimSpace(string(snippet)))
}
