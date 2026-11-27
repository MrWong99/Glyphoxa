package elevenlabs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
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
//
// Keyterms fail open (#701). A 400/422 answer to a request that carried keyterms
// is retried once, at once, WITHOUT them: if that succeeds the list was the
// reason, the utterance is transcribed unbiased instead of lost, and the client
// sends no keyterms again ([Client.refuseKeyterms]). If the retry is refused too
// the list was not the reason; that error is returned and the list is kept.
func (c *Client) Transcribe(ctx context.Context, frames []audio.Frame) (stt.Transcript, error) {
	if c.apiKey == "" {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	terms := c.liveKeyterms(c.terms)
	tr, err := c.post(ctx, frames, terms)
	if err == nil || len(terms) == 0 || !isRequestRefusal(err) {
		return tr, err
	}
	tr, retryErr := c.post(ctx, frames, nil)
	if retryErr != nil {
		return stt.Transcript{}, retryErr
	}
	c.refuseKeyterms("batch", len(terms), err.Error())
	return tr, nil
}

// isRequestRefusal reports whether err is the provider rejecting the request's
// parameters (HTTP 400 or 422) — the answer to a keyterm it does not accept —
// rather than a failure of auth, quota, rate or the service.
func isRequestRefusal(err error) bool {
	var he *providererr.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.StatusCode == http.StatusBadRequest || he.StatusCode == http.StatusUnprocessableEntity
}

// post sends one transcription request carrying keyterms (already sanitised;
// none sends the pre-#701 request).
func (c *Client) post(ctx context.Context, frames []audio.Frame, keyterms []string) (stt.Transcript, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("model_id", Model); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write model_id: %w", err)
	}
	if err := mw.WriteField("file_format", FileFormatPCMS16LE16); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write file_format: %w", err)
	}
	// Turn off the server-side work we never consume — we read only Transcript.Text,
	// so both of these shrink the scribe POST's latency (the dominant fixed cost
	// inside response_latency) without changing the text we act on. diarize defaults
	// off; language stays auto-detected unless [WithLanguage] pins it, since the
	// user may speak EN or DE.
	//
	// timestamps_granularity=none skips word/character forced-alignment, a
	// non-trivial server cost the orchestrator has no use for (it carries no
	// per-word timing).
	if err := mw.WriteField("timestamps_granularity", "none"); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write timestamps_granularity: %w", err)
	}
	// tag_audio_events=false skips non-speech event detection ([laughter], etc.),
	// keeping the transcript to the spoken words the LLM and address matcher want.
	if err := mw.WriteField("tag_audio_events", "false"); err != nil {
		return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write tag_audio_events: %w", err)
	}
	// Recognition hints (#701). Keyterms bias the transcript towards the names at
	// the table; one repeated field per term, already sanitised by [WithTerms].
	// language_code is sent only when the caller opted in — the default stays
	// auto-detect.
	for _, term := range keyterms {
		if err := mw.WriteField("keyterms", term); err != nil {
			return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write keyterms: %w", err)
		}
	}
	if c.language != "" {
		if err := mw.WriteField("language_code", c.language); err != nil {
			return stt.Transcript{}, fmt.Errorf("elevenlabs.Transcribe: write language_code: %w", err)
		}
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

// readErrorResponse is [providererr.FromResponse] with the adapter's "elevenlabs." Op prefix.
func readErrorResponse(resp *http.Response, op string) error {
	return providererr.FromResponse(resp, "elevenlabs."+op)
}
