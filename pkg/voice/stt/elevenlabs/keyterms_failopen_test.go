package elevenlabs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// The keyterm list is tenant free text that only this adapter's sanitiser stands
// in front of. These tests pin what happens when the provider refuses a list
// anyway (#701 review): the adapter FAILS OPEN — it stops sending keyterms and
// keeps transcribing — instead of losing every utterance until an operator
// flips GLYPHOXA_STT_KEYTERMS.

// pickyScribe is a batch endpoint that answers a request with status refuse when
// refuses(request) says so, and transcribes everything else. It records, per
// request, the keyterms it carried.
type pickyScribe struct {
	*httptest.Server
	mu   sync.Mutex
	seen [][]string
}

func newPickyScribe(t *testing.T, refuse int, refuses func(keyterms []string) bool) *pickyScribe {
	t.Helper()
	p := &pickyScribe{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		terms := r.MultipartForm.Value["keyterms"]
		p.mu.Lock()
		p.seen = append(p.seen, terms)
		refused := refuses(terms) // under the lock: refuses may keep state
		p.mu.Unlock()
		if refused {
			// The body the real endpoint sends for an over-long keyterm (2026-11-22).
			w.WriteHeader(refuse)
			_, _ = w.Write([]byte(`{"detail":{"type":"validation_error","code":"invalid_parameters","message":"All keywords must be at most 50 characters.","status":"invalid_keyword_length","param":"keywords"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"text":"Flo, stopp"}`))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pickyScribe) requests() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.seen...)
}

func oneFrame(t *testing.T) []audio.Frame {
	t.Helper()
	return []audio.Frame{frame16k32ms(t, make([]int16, 512))}
}

func hasKeyterms(terms []string) bool { return len(terms) > 0 }

// TestTranscribe_KeytermsRefused_FailsOpen: the provider answers the keyterm
// list with a 400. The utterance is NOT lost — the same audio is transcribed
// without keyterms in the same call — and no later request of this client
// carries keyterms again.
func TestTranscribe_KeytermsRefused_FailsOpen(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			scribe := newPickyScribe(t, status, hasKeyterms)
			var logs bytes.Buffer
			c := elevenlabs.New("k",
				elevenlabs.WithBaseURL(scribe.URL),
				elevenlabs.WithTerms([]string{"Flo", "Artusas"}),
				elevenlabs.WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))))

			for i := range 3 {
				got, err := c.Transcribe(context.Background(), oneFrame(t))
				if err != nil {
					t.Fatalf("utterance %d was lost to the refused keyterm list: %v", i, err)
				}
				if got.Text != "Flo, stopp" {
					t.Errorf("utterance %d text = %q", i, got.Text)
				}
			}

			reqs := scribe.requests()
			if len(reqs) != 4 {
				t.Fatalf("want 4 requests (refused, its retry, then two plain), got %d: %q", len(reqs), reqs)
			}
			if !hasKeyterms(reqs[0]) {
				t.Errorf("the first request must offer the keyterms, got %q", reqs[0])
			}
			for i, terms := range reqs[1:] {
				if hasKeyterms(terms) {
					t.Errorf("request %d still carried the refused keyterms %q", i+1, terms)
				}
			}

			out := logs.String()
			if n := strings.Count(out, "level=WARN"); n != 1 {
				t.Errorf("want exactly one warning for the whole session, got %d:\n%s", n, out)
			}
			for _, name := range []string{"Flo", "Artusas"} {
				if strings.Contains(out, name) {
					t.Errorf("the warning names a keyterm (%q):\n%s", name, out)
				}
			}
		})
	}
}

// TestTranscribe_RefusalNotAboutKeyterms_KeepsThem: a 400 the provider repeats
// for the same audio WITHOUT keyterms was not about the keyterms. The error is
// returned as before and the next utterance is still biased.
func TestTranscribe_RefusalNotAboutKeyterms_KeepsThem(t *testing.T) {
	refuseFirstTwo := 0
	scribe := newPickyScribe(t, http.StatusBadRequest, func([]string) bool {
		refuseFirstTwo++
		return refuseFirstTwo <= 2
	})
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(scribe.URL), elevenlabs.WithTerms([]string{"Flo"}))

	_, err := c.Transcribe(context.Background(), oneFrame(t))
	var he *providererr.HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusBadRequest {
		t.Fatalf("Transcribe = %v, want the provider's 400", err)
	}
	if _, err := c.Transcribe(context.Background(), oneFrame(t)); err != nil {
		t.Fatalf("second utterance: %v", err)
	}
	reqs := scribe.requests()
	if len(reqs) != 3 || !hasKeyterms(reqs[2]) {
		t.Errorf("the utterance after an unrelated 400 must still carry keyterms; requests: %q", reqs)
	}
}

// TestTranscribe_ServerErrorWithKeyterms_IsNotARefusal: a 5xx says nothing about
// the keyterm list. It is not retried without it here (the STT stage's retry
// policy owns transient failures) and the list stays.
func TestTranscribe_ServerErrorWithKeyterms_IsNotARefusal(t *testing.T) {
	first := true
	scribe := newPickyScribe(t, http.StatusServiceUnavailable, func([]string) bool {
		was := first
		first = false
		return was
	})
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(scribe.URL), elevenlabs.WithTerms([]string{"Flo"}))

	if _, err := c.Transcribe(context.Background(), oneFrame(t)); err == nil {
		t.Fatal("want the 503 surfaced")
	}
	if _, err := c.Transcribe(context.Background(), oneFrame(t)); err != nil {
		t.Fatalf("second utterance: %v", err)
	}
	reqs := scribe.requests()
	if len(reqs) != 2 || !hasKeyterms(reqs[1]) {
		t.Errorf("want two requests, both with keyterms; got %q", reqs)
	}
}

// refusedDetail is the realtime endpoint's answer to a keyterm list one character
// over its budget, verbatim (measured 2026-11-22).
const refusedDetail = "Total length of keyterms cannot exceed 1000 characters. You provided 1001 characters."

// closedCause waits for a session's OnClosed and returns its cause.
func closedCause(t *testing.T, closed <-chan error) *stt.StreamError {
	t.Helper()
	select {
	case err := <-closed:
		return asStreamError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("the session was not closed")
		return nil
	}
}

// TestOpenStream_KeytermsRefused_FailsOpen: the realtime endpoint answers a
// keyterm list it refuses with invalid_request where session_started should be.
// The adapter reports that session dead with [stt.CodeTermsRefused] — a cause the
// caller redials promptly, not a durable misconfiguration — and the redial, given
// the very same terms, goes out without them and starts. The batch path of the
// same client drops them too: one session, one list, on both transports.
func TestOpenStream_KeytermsRefused_FailsOpen(t *testing.T) {
	var (
		mu    sync.Mutex
		dials [][]string
		posts [][]string
	)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/speech-to-text/realtime", func(w http.ResponseWriter, r *http.Request) {
		terms := r.URL.Query()["keyterms"]
		mu.Lock()
		dials = append(dials, terms)
		mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if hasKeyterms(terms) {
			_ = conn.WriteJSON(errorMsg("invalid_request", refusedDetail))
		} else {
			_ = conn.WriteJSON(map[string]any{"message_type": "session_started"})
		}
		echoUntilClose(conn)
	})
	mux.HandleFunc("/v1/speech-to-text", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		mu.Lock()
		posts = append(posts, r.MultipartForm.Value["keyterms"])
		mu.Unlock()
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var logs bytes.Buffer
	terms := []string{"Flo", "Artusas"}
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL), elevenlabs.WithTerms(terms),
		elevenlabs.WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))))

	closed := make(chan error, 1)
	refused, err := c.OpenStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000, Terms: terms, OnClosed: func(err error) { closed <- err }})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer refused.Close()
	cause := closedCause(t, closed)
	if cause.Code != stt.CodeTermsRefused || !cause.Fatal {
		t.Fatalf("OnClosed cause = %v, want a fatal %s", cause, stt.CodeTermsRefused)
	}

	stillUp := make(chan error, 1)
	redial, err := c.OpenStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000, Terms: terms, OnClosed: func(err error) { stillUp <- err }})
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	defer redial.Close()
	if _, err := c.Transcribe(context.Background(), oneFrame(t)); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	select {
	case err := <-stillUp:
		t.Fatalf("the redialled session died too: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dials) != 2 || !hasKeyterms(dials[0]) || hasKeyterms(dials[1]) {
		t.Errorf("want a dial with keyterms, then one without; got %q", dials)
	}
	if len(posts) != 1 || hasKeyterms(posts[0]) {
		t.Errorf("the batch path must drop the refused keyterms as well; got %q", posts)
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Errorf("want exactly one warning, got %d:\n%s", n, out)
	}
}

// TestOpenStream_InvalidRequest_NotAKeytermRefusal: invalid_request only means
// "the keyterms were refused" when keyterms were sent and the session had not
// started. Without keyterms, or once session_started has arrived, it stays the
// durable invalid_request it was — and a list is not given up over it.
func TestOpenStream_InvalidRequest_NotAKeytermRefusal(t *testing.T) {
	cases := []struct {
		name    string
		terms   []string
		started bool
	}{
		{"no keyterms were sent", nil, false},
		{"the session had already started", []string{"Flo"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := newScriptedServer(t, func(conn *websocket.Conn) {
				if tc.started {
					_ = conn.WriteJSON(map[string]any{"message_type": "session_started"})
				}
				_ = conn.WriteJSON(errorMsg("invalid_request", "nope"))
				echoUntilClose(conn)
			})
			defer ss.Close()

			c := elevenlabs.New("k", elevenlabs.WithBaseURL(ss.URL))
			closed := make(chan error, 1)
			s, err := c.OpenStream(context.Background(), stt.StreamConfig{
				SampleRate: 16000, Terms: tc.terms, OnClosed: func(err error) { closed <- err }})
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			defer s.Close()
			if cause := closedCause(t, closed); cause.Code != "invalid_request" {
				t.Errorf("cause = %v, want invalid_request", cause)
			}

			again, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 16000, Terms: tc.terms})
			if err != nil {
				t.Fatalf("redial: %v", err)
			}
			defer again.Close()
			if got := ss.lastReq.Load().query["keyterms"]; len(got) != len(tc.terms) {
				t.Errorf("the redial carried keyterms %q, want the session's %q", got, tc.terms)
			}
		})
	}
}
