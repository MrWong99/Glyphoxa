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

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
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
