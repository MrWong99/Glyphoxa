package elevenlabs_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// TestKeyterms_Sanitises pins the one sanitiser both STT paths send their
// keyterms through (#701): what the provider rejects never leaves the process,
// and the order the caller gave — its priority order — survives.
func TestKeyterms_Sanitises(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays empty", nil, nil},
		{"trims and drops empties", []string{"  Flo ", "", "   ", "Bart"}, []string{"Flo", "Bart"}},
		{"strips the forbidden characters", []string{`F<l>o`, `{Bart}`, `[Mira]`, `Jä\ger`, "<>{}[]\\"}, []string{"Flo", "Bart", "Mira", "Jäger"}},
		{"collapses inner whitespace", []string{"Old   Tom\tthe  Wise"}, []string{"Old Tom the Wise"}},
		{"keeps the first five words", []string{"one two three four five six seven"}, []string{"one two three four five"}},
		{"cuts a term at fifty characters", []string{strings.Repeat("ä", 60)}, []string{strings.Repeat("ä", 50)}},
		{"a cut never leaves a trailing space", []string{strings.Repeat("a", 49) + " bcd"}, []string{strings.Repeat("a", 49)}},
		{"de-duplicates case-insensitively, first spelling wins", []string{"Flo", "flo", "FLO", "Bart", "Flo "}, []string{"Flo", "Bart"}},
		{"de-duplicates after sanitising", []string{"Flo", "<Flo>"}, []string{"Flo"}},
		// A term the recognizer cannot hear — punctuation only, or nothing but
		// invisible characters — biases nothing and is the likeliest thing for the
		// provider to refuse.
		{"drops a term with no letter or digit", []string{"???", "-", "​", "…", "Flo", "R2-D2", "42"}, []string{"Flo", "R2-D2", "42"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := elevenlabs.Keyterms(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Keyterms(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestKeyterms_CapsTheListAtFiftyTerms: the realtime endpoint answers a 51st term
// with invalid_request and the session never starts (measured 2026-11-22), so the
// list is cut from its low-priority end.
func TestKeyterms_CapsTheListAtFiftyTerms(t *testing.T) {
	var in []string
	for i := range 80 {
		in = append(in, fmt.Sprintf("Name%02d", i))
	}
	got := elevenlabs.Keyterms(in)
	if len(got) != elevenlabs.MaxKeyterms {
		t.Fatalf("len = %d, want %d", len(got), elevenlabs.MaxKeyterms)
	}
	if !reflect.DeepEqual(got, in[:elevenlabs.MaxKeyterms]) {
		t.Errorf("the cap must keep the head of the list, got %q", got)
	}
}

// TestKeyterms_CapsTheListAtOneThousandCharacters: the second hard realtime
// limit. The budget is counted in bytes, which can only be stricter than the
// provider's character count.
func TestKeyterms_CapsTheListAtOneThousandCharacters(t *testing.T) {
	var in []string
	for i := range 30 { // 30 distinct 40-byte terms = 1200 bytes
		in = append(in, fmt.Sprintf("%02d%s", i, strings.Repeat("x", 38)))
	}
	got := elevenlabs.Keyterms(in)
	if !reflect.DeepEqual(got, in[:25]) {
		t.Errorf("want the first 25 terms (1000 bytes), got %d: %q", len(got), got)
	}

	// A multi-byte term counts by its bytes: 20 terms of 50 two-byte runes are
	// 2000 bytes, so only ten fit.
	in = in[:0]
	for i := range 20 {
		in = append(in, string(rune('a'+i))+strings.Repeat("ä", 49))
	}
	total := 0
	for _, term := range elevenlabs.Keyterms(in) {
		total += len(term)
	}
	if total > elevenlabs.MaxKeytermsChars {
		t.Errorf("sent %d bytes of keyterms, limit %d", total, elevenlabs.MaxKeytermsChars)
	}
}

// TestKeyterms_TotalCapBoundary pins the 1000-character budget AT its edge. The
// realtime endpoint was measured on both sides of it (2026-11-22): a list of
// exactly 1000 characters starts a session, 1001 is answered with
// invalid_request.
func TestKeyterms_TotalCapBoundary(t *testing.T) {
	distinct := func(n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%02d%s", i, strings.Repeat("x", size-2))
		}
		return out
	}

	t.Run("exactly 1000 is sent whole", func(t *testing.T) {
		in := distinct(20, 50)
		if got := elevenlabs.Keyterms(in); !reflect.DeepEqual(got, in) {
			t.Errorf("20 terms of 50 (1000) must all be kept, got %d", len(got))
		}
	})
	t.Run("the 1001st character is not", func(t *testing.T) {
		in := append(distinct(25, 40), "z")
		if got := elevenlabs.Keyterms(in); !reflect.DeepEqual(got, in[:25]) {
			t.Errorf("want the 25 terms that make 1000, got %d: %q", len(got), got[len(got)-1])
		}
	})
	t.Run("the list ends at the first term that does not fit", func(t *testing.T) {
		// 960 + 50 overflows; the 2-byte term after it would fit, but the list is
		// a priority order and is cut, not searched for something smaller.
		in := append(distinct(24, 40), strings.Repeat("y", 50), "ab")
		if got := elevenlabs.Keyterms(in); !reflect.DeepEqual(got, in[:24]) {
			t.Errorf("want the first 24 terms, got %d: %q", len(got), got[len(got)-1])
		}
	})
}

// batchForm runs one Transcribe against a recording server and returns the
// multipart text fields the adapter posted.
func batchForm(t *testing.T, opts ...elevenlabs.Option) map[string][]string {
	t.Helper()
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		form = r.MultipartForm.Value
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("k", append([]elevenlabs.Option{elevenlabs.WithBaseURL(srv.URL)}, opts...)...)
	if _, err := c.Transcribe(context.Background(), []audio.Frame{frame16k32ms(t, make([]int16, 512))}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	return form
}

// TestTranscribe_SendsKeytermsAsRepeatedField: the batch path biases Scribe
// towards the client's terms with one multipart "keyterms" field per term,
// sanitised (#701).
func TestTranscribe_SendsKeytermsAsRepeatedField(t *testing.T) {
	form := batchForm(t, elevenlabs.WithTerms([]string{"Flo", " <Bart> ", "flo", "Jäger"}))
	if got, want := form["keyterms"], []string{"Flo", "Bart", "Jäger"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keyterms = %q, want %q", got, want)
	}
}

// TestTranscribe_NoHints_RequestUnchanged: without terms or a language the
// request carries neither field — the pre-#701 request, and no keyterm surcharge.
func TestTranscribe_NoHints_RequestUnchanged(t *testing.T) {
	form := batchForm(t)
	for _, field := range []string{"keyterms", "language_code"} {
		if got, ok := form[field]; ok {
			t.Errorf("unexpected %s field %q", field, got)
		}
	}
}

// TestTranscribe_SendsLanguageCode: the opt-in language hint reaches the batch
// request as language_code.
func TestTranscribe_SendsLanguageCode(t *testing.T) {
	form := batchForm(t, elevenlabs.WithLanguage("de"))
	if got, want := form["language_code"], []string{"de"}; !reflect.DeepEqual(got, want) {
		t.Errorf("language_code = %q, want %q", got, want)
	}
}

// TestOpenStream_SendsKeytermsAsRepeatedQueryParam: the realtime path carries the
// session's terms as one "keyterms" query parameter per term, through the same
// sanitiser as the batch path (#701).
func TestOpenStream_SendsKeytermsAsRepeatedQueryParam(t *testing.T) {
	ss := newScriptedServer(t, echoUntilClose)
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000, Terms: []string{"Flo", " <Bart> ", "flo", "Jäger"}})
	defer s.Close()

	if got, want := ss.lastReq.Load().query["keyterms"], []string{"Flo", "Bart", "Jäger"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keyterms = %q, want %q", got, want)
	}
}

// TestOpenStream_NoTerms_NoKeytermsParam: a session without terms dials the
// pre-#701 URL — and is not billed the keyterm surcharge.
func TestOpenStream_NoTerms_NoKeytermsParam(t *testing.T) {
	ss := newScriptedServer(t, echoUntilClose)
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000})
	defer s.Close()

	if got, ok := ss.lastReq.Load().query["keyterms"]; ok {
		t.Errorf("unexpected keyterms %q", got)
	}
}

// TestOpenStream_OversizedTermList_StaysInsideTheHardLimits: the realtime
// endpoint refuses a session outright past 50 terms or 1000 characters, so no
// caller-supplied list may reach the wire unbounded.
func TestOpenStream_OversizedTermList_StaysInsideTheHardLimits(t *testing.T) {
	ss := newScriptedServer(t, echoUntilClose)
	defer ss.Close()

	var terms []string
	for i := range 200 {
		terms = append(terms, fmt.Sprintf("Name%03d", i))
	}
	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000, Terms: terms})
	defer s.Close()

	if got := ss.lastReq.Load().query["keyterms"]; len(got) != elevenlabs.MaxKeyterms {
		t.Errorf("sent %d keyterms, want %d", len(got), elevenlabs.MaxKeyterms)
	}
}

// TestErrorFrame_InvalidRequest_InsteadOfSessionStarted_IsFatal pins what the
// provider does with a configuration it rejects (measured 2026-11-22: a 51st
// keyterm): the upgrade succeeds, then an invalid_request frame arrives where
// session_started should, and the session is dead.
func TestErrorFrame_InvalidRequest_InsteadOfSessionStarted_IsFatal(t *testing.T) {
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(errorMsg("invalid_request", "Too many keyterms"))
		echoUntilClose(conn)
	})
	defer ss.Close()

	closed := make(chan error, 1)
	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000, OnClosed: func(err error) { closed <- err }})
	defer s.Close()

	select {
	case err := <-closed:
		se := asStreamError(t, err)
		if se.Code != "invalid_request" || !se.Fatal {
			t.Errorf("OnClosed cause = %v, want a fatal invalid_request", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session survived an invalid_request frame")
	}
}
