//go:build live

// Live HTTP-vs-websocket comparison for #702. Excluded from the default keyless
// suite by the `live` tag — it makes PAID calls to the real ElevenLabs endpoint
// (about 7k characters on eleven_v4_turbo) and runs only with
// `go test -tags=live -run TestLive_Transport` and ELEVENLABS_API_KEY set.
//
// Everything goes through the real [Client] the way the live pipeline drives it:
// a persisted default voice ([DefaultVoice], pcm_48000), one sentence per call.
// It prints the table the ADR-0023 amendment quotes.
package elevenlabs_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// transportLines are the sentences every arm renders (n = 10 per arm).
var transportLines = []string{
	"Willkommen im Gasthaus zum Grünen Drachen, Reisender. Was darf es sein?",
	"Ein Zimmer kostet drei Silberlinge die Nacht, das Frühstück ist inbegriffen.",
	"Hütet euch vor dem Moor im Norden, dort spuken seit Wochen die Irrlichter.",
	"Der Schmied hat gestern eine Lieferung Zwergenstahl aus den Bergen bekommen.",
	"Wenn ihr den Fährmann sucht, der sitzt um diese Zeit meist unten am Steg.",
	"Die Stadtwache hat das Osttor nach Einbruch der Dunkelheit geschlossen.",
	"Vor drei Nächten ist ein Fremder mit einer versiegelten Kiste hier abgestiegen.",
	"Setzt euch ans Feuer, der Eintopf ist gleich fertig und das Bier ist frisch.",
	"Niemand, der in die alte Mine hinabgestiegen ist, kam je wieder heraus.",
	"Der Baron zahlt gut für jeden, der ihm den Kopf des Räuberhauptmanns bringt.",
}

// transportLongLine is one sentence of ~20 s of audio, for the barge case and
// the back-pressured read.
const transportLongLine = "Hört gut zu, denn ich erzähle es nur ein einziges Mal: hinter dem Wasserfall " +
	"am Ende der Schlucht liegt eine Höhle, in der die alten Könige ihren Schatz verborgen haben, " +
	"bewacht von einem Wesen, das weder schläft noch altert, und wer es wagt, die Schwelle zu " +
	"überschreiten, ohne das Losungswort zu kennen, den findet man am nächsten Morgen mit weißem Haar " +
	"und leerem Blick am Ufer des Flusses wieder."

type latencies []time.Duration

func (l latencies) row(label string) string {
	if len(l) == 0 {
		return fmt.Sprintf("| %s | 0 | – | – | – | – |", label)
	}
	s := append(latencies(nil), l...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p90 := s[(len(s)*9+9)/10-1]
	return fmt.Sprintf("| %s | %d | %d | %d | %d | %d |", label, len(s),
		s[len(s)/2].Milliseconds(), s[0].Milliseconds(), p90.Milliseconds(), s[len(s)-1].Milliseconds())
}

// firstChunk starts one sentence and reports the time to its first audio chunk,
// handing back the channel (first chunk consumed) for the caller to finish.
func firstChunk(t *testing.T, c *elevenlabs.Client, ctx context.Context, voice tts.Voice, text string) (time.Duration, <-chan tts.AudioChunk) {
	t.Helper()
	d, ch, err := tryFirstChunk(c, ctx, voice, text)
	if err != nil {
		t.Fatalf("Synthesize(%q): %v", text, err)
	}
	return d, ch
}

// tryFirstChunk is firstChunk without the test handle, for goroutines.
func tryFirstChunk(c *elevenlabs.Client, ctx context.Context, voice tts.Voice, text string) (time.Duration, <-chan tts.AudioChunk, error) {
	start := time.Now()
	ch, err := c.Synthesize(ctx, tts.SynthesizeRequest{Sentence: text, Voice: voice})
	if err != nil {
		return 0, nil, err
	}
	chunk, ok := <-ch
	if !ok {
		return 0, nil, fmt.Errorf("stream closed before any audio")
	}
	if chunk.Err != nil {
		return 0, nil, chunk.Err
	}
	return time.Since(start), ch, nil
}

func drain(t *testing.T, ch <-chan tts.AudioChunk) (bytes int) {
	t.Helper()
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("mid-stream: %v", chunk.Err)
		}
		bytes += len(chunk.PCM)
	}
	return bytes
}

func TestLive_TransportComparison(t *testing.T) {
	if os.Getenv(elevenlabs.APIKeyEnv) == "" {
		t.Skipf("%s not set", elevenlabs.APIKeyEnv)
	}
	ctx := context.Background()
	voice := liveVoice(t, "")
	newWS := func() *elevenlabs.Client { return elevenlabs.New("", elevenlabs.WithTransport(elevenlabs.TransportWS)) }

	httpC := elevenlabs.New("")
	defer httpC.Close()
	wsC := newWS()
	defer wsC.Close()

	// Prime both warm arms: the HTTP pool's connection and the voice's socket.
	if err := httpC.Warm(ctx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	_, ch := firstChunk(t, httpC, ctx, voice, "Einen Augenblick, ich bin gleich bei euch.")
	drain(t, ch)
	_, ch = firstChunk(t, wsC, ctx, voice, "Einen Augenblick, ich bin gleich bei euch.")
	drain(t, ch)

	// --- first chunk, sequential sentences ---
	var httpWarm, wsWarm, wsFresh latencies
	arms := []func(text string){
		func(text string) {
			d, ch := firstChunk(t, httpC, ctx, voice, text)
			drain(t, ch)
			httpWarm = append(httpWarm, d)
		},
		func(text string) {
			d, ch := firstChunk(t, wsC, ctx, voice, text)
			drain(t, ch)
			wsWarm = append(wsWarm, d)
		},
		func(text string) {
			c := newWS() // nothing pooled: the sentence dials its socket
			d, ch := firstChunk(t, c, ctx, voice, text)
			drain(t, ch)
			_ = c.Close()
			wsFresh = append(wsFresh, d)
		},
	}
	for i, text := range transportLines {
		for k := range arms { // rotate the order so no arm always goes first on a text
			arms[(i+k)%len(arms)](text)
		}
	}
	if st := wsC.WSStats(); st.Dials != 1 || st.Fallbacks != 0 {
		t.Errorf("warm websocket arm: stats %+v, want every sentence on the one socket", st)
	}

	// --- look-ahead pair: k+1 starts while k is unread ---
	var httpPairK, httpPairNext, wsPairK, wsPairNext latencies
	pair := func(c *elevenlabs.Client, a, b string) (time.Duration, time.Duration) {
		var wg sync.WaitGroup
		var dA, dB time.Duration
		var chA, chB <-chan tts.AudioChunk
		var errA, errB error
		wg.Add(2)
		go func() { defer wg.Done(); dA, chA, errA = tryFirstChunk(c, ctx, voice, a) }()
		go func() { defer wg.Done(); dB, chB, errB = tryFirstChunk(c, ctx, voice, b) }()
		wg.Wait()
		if errA != nil || errB != nil {
			t.Fatalf("look-ahead pair: %v / %v", errA, errB)
		}
		drain(t, chA)
		drain(t, chB)
		return dA, dB
	}
	pair(wsC, transportLines[0], transportLines[1]) // opens the voice's second socket
	for i := 0; i < 5; i++ {
		a, b := transportLines[(2*i)%10], transportLines[(2*i+1)%10]
		dA, dB := pair(httpC, a, b)
		httpPairK, httpPairNext = append(httpPairK, dA), append(httpPairNext, dB)
		dA, dB = pair(wsC, a, b)
		wsPairK, wsPairNext = append(wsPairK, dA), append(wsPairNext, dB)
	}
	if st := wsC.WSStats(); st.Dials != 2 || st.Fallbacks != 0 {
		t.Errorf("websocket look-ahead pairs: stats %+v, want two sockets and no HTTP fallback", st)
	}

	// --- barge, then the next reply's first sentence ---
	// The cut sentence takes its connection with it on either transport. The
	// wiring calls Warm on the barge; the next sentence follows after STT + LLM.
	var httpBarge, wsBarge latencies
	barge := func(c *elevenlabs.Client, next string) time.Duration {
		cut, cancel := context.WithCancel(ctx)
		_, ch := firstChunk(t, c, cut, voice, transportLongLine)
		cancel()
		for range ch {
		}
		_ = c.Warm(ctx)
		time.Sleep(600 * time.Millisecond)
		d, ch := firstChunk(t, c, ctx, voice, next)
		drain(t, ch)
		return d
	}
	for i := 0; i < 5; i++ {
		httpBarge = append(httpBarge, barge(httpC, transportLines[i]))
		wsBarge = append(wsBarge, barge(wsC, transportLines[i]))
	}

	// --- one long sentence read at playback pace over the websocket ---
	// Playback back-pressure holds the socket for the sentence's whole duration
	// (past the server's 20 s inactivity limit); keep_alive must carry it.
	start := time.Now()
	_, ch = firstChunk(t, wsC, ctx, voice, transportLongLine)
	var paced int
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("paced read failed after %v: %v", time.Since(start), chunk.Err)
		}
		paced += len(chunk.PCM)
		time.Sleep(time.Duration(len(chunk.PCM)/2) * time.Second / 48000)
	}
	pacedAudio := time.Duration(paced/2) * time.Second / 48000

	var b strings.Builder
	b.WriteString("\n| first audio chunk, ms | n | median | min | p90 | max |\n|---|---|---|---|---|---|\n")
	for _, r := range []string{
		httpWarm.row("HTTP, warm connection"),
		wsWarm.row("WS, warm socket"),
		wsFresh.row("WS, fresh dial per sentence"),
		httpPairK.row("look-ahead pair over HTTP: sentence k"),
		httpPairNext.row("look-ahead pair over HTTP: sentence k+1"),
		wsPairK.row("look-ahead pair over WS: sentence k"),
		wsPairNext.row("look-ahead pair over WS: sentence k+1"),
		httpBarge.row("HTTP: first sentence 600 ms after a barge"),
		wsBarge.row("WS: first sentence 600 ms after a barge"),
	} {
		b.WriteString(r + "\n")
	}
	fmt.Fprintf(&b, "paced websocket read: %.1f s of audio read over %.1f s, no error\n", pacedAudio.Seconds(), time.Since(start).Seconds())
	fmt.Fprintf(&b, "websocket client: %+v\n", wsC.WSStats())
	t.Log(b.String())
}
