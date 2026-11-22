//go:build live

// Live proof that keyterms change what Scribe hears (#701). Excluded from the
// default keyless suite by the `live` tag: it calls the real ElevenLabs API on
// both STT transports and needs ELEVENLABS_API_KEY plus a directory of short
// 16 kHz mono s16le WAV clips of somebody addressing an NPC called "Flo".
//
//	set -a; . ./.env; set +a
//	GLYPHOXA_STT_KEYTERM_CLIPS=/path/to/clips GLYPHOXA_STT_KEYTERM_LIST='Flo|Mira|Bart' \
//	  go test -tags=live -run TestLive_Keyterms -v ./pkg/voice/stt/elevenlabs/
package elevenlabs_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// wavFrames loads a 16 kHz mono s16le WAV as 32 ms frames, zero-padding the last.
func wavFrames(t *testing.T, path string) []audio.Frame {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	i := bytes.Index(raw, []byte("data"))
	if i < 0 || i+8 > len(raw) {
		t.Fatalf("%s: no data chunk", path)
	}
	pcm := raw[i+8:]
	var frames []audio.Frame
	for off := 0; off < len(pcm); off += 1024 {
		samples := make([]int16, 512)
		for j := 0; j < 512 && off+2*j+1 < len(pcm); j++ {
			samples[j] = int16(binary.LittleEndian.Uint16(pcm[off+2*j:]))
		}
		f, err := audio.NewFrame(samples, 16000, 32)
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		frames = append(frames, f)
	}
	return frames
}

// The realtime half streams the way the voice loop does (see the realtime
// closure): a session that is already established, and silence around the speech.
const (
	sessionSettle  = 500 * time.Millisecond
	preRollFrames  = 6  // ~200 ms
	hangoverFrames = 12 // ~384 ms, the VAD hangover
)

func silentFrames(t *testing.T, n int) []audio.Frame {
	t.Helper()
	frames := make([]audio.Frame, n)
	for i := range frames {
		f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		frames[i] = f
	}
	return frames
}

// saysName reports whether transcript contains name as a whole word.
func saysName(transcript, name string) bool {
	for _, word := range strings.FieldsFunc(strings.ToLower(transcript), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if word == strings.ToLower(name) {
			return true
		}
	}
	return false
}

// TestLive_Keyterms_BothTransports transcribes each clip three times per
// transport without keyterms and three times with the session's list, through
// the real adapter, and prints the table. With the keyterm every run must contain the
// name; without it the transcript is only reported — that half is the provider's
// unbiased behaviour, which is what this feature exists to correct.
func TestLive_Keyterms_BothTransports(t *testing.T) {
	if os.Getenv("ELEVENLABS_API_KEY") == "" {
		t.Skip("ELEVENLABS_API_KEY not set; skipping live keyterm probe")
	}
	dir := os.Getenv("GLYPHOXA_STT_KEYTERM_CLIPS")
	if dir == "" {
		t.Skip("GLYPHOXA_STT_KEYTERM_CLIPS not set; skipping live keyterm probe")
	}
	const (
		name = "Flo"
		runs = 3
	)
	clips := []string{"07-kurz.wav", "08-stopp.wav"}
	// The smallest list a Voice Session sends: one NPC and the Butler. Override it
	// to probe others — notably the name ALONE, which measured weaker on the
	// realtime transport (2026-11-22: a lone "Flo?" was heard 2 times in 9 with
	// ["Flo"] and 6 in 6 with a two- or nine-term list; batch heard it every time).
	terms := []string{name, "Glyphoxa"}
	if list := os.Getenv("GLYPHOXA_STT_KEYTERM_LIST"); list != "" {
		terms = strings.Split(list, "|")
	}

	batch := func(t *testing.T, frames []audio.Frame, terms []string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tr, err := elevenlabs.New("", elevenlabs.WithTerms(terms)).Transcribe(ctx, frames)
		if err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		return tr.Text
	}
	realtime := func(t *testing.T, frames []audio.Frame, terms []string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s, err := elevenlabs.New("").OpenStream(ctx, stt.StreamConfig{SampleRate: 16000, Terms: terms})
		if err != nil {
			t.Fatalf("OpenStream: %v", err)
		}
		defer s.Close()
		// What a Speaker Lane sends: the session is already up when speech starts,
		// and the utterance arrives wrapped in the VAD's pre-roll and hangover.
		time.Sleep(sessionSettle)
		wrapped := append(append(silentFrames(t, preRollFrames), frames...), silentFrames(t, hangoverFrames)...)
		tick := time.NewTicker(32 * time.Millisecond)
		defer tick.Stop()
		for _, f := range wrapped {
			if err := s.Send(f); err != nil {
				t.Fatalf("Send: %v", err)
			}
			<-tick.C
		}
		ch, err := s.Commit()
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		select {
		case res := <-ch:
			if res.Err != nil {
				t.Fatalf("commit: %v", res.Err)
			}
			return res.Transcript.Text
		case <-time.After(20 * time.Second):
			t.Fatal("commit did not resolve within 20s")
			return ""
		}
	}

	var table strings.Builder
	table.WriteString("\n| clip | transport | keyterms | run 1 | run 2 | run 3 | name heard |\n|---|---|---|---|---|---|---|\n")
	for _, clip := range clips {
		frames := wavFrames(t, filepath.Join(dir, clip))
		for _, tr := range []struct {
			label string
			run   func(*testing.T, []audio.Frame, []string) string
		}{{"batch", batch}, {"realtime", realtime}} {
			for _, withTerms := range []bool{false, true} {
				var use []string
				label := "none"
				if withTerms {
					use, label = terms, strings.Join(terms, ", ")
				}
				heard := 0
				row := fmt.Sprintf("| %s | %s | %s |", clip, tr.label, label)
				for range runs {
					text := tr.run(t, frames, use)
					if saysName(text, name) {
						heard++
					}
					row += fmt.Sprintf(" %q |", text)
				}
				table.WriteString(fmt.Sprintf("%s %d/%d |\n", row, heard, runs))
				if withTerms && heard != runs {
					t.Errorf("%s over %s with keyterms %q: the name was heard in %d of %d runs", clip, tr.label, terms, heard, runs)
				}
			}
		}
	}
	t.Log(table.String())
}
