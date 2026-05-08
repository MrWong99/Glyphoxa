package voicetest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Clip is a decoded test audio clip from tests/voice-clips/<name>/audio.wav.
type Clip struct {
	Name       string
	SampleRate int
	Channels   int
	BitDepth   int

	// PCM holds the raw audio samples in the encoding implied by SampleRate,
	// Channels, and BitDepth (little-endian for our fixtures).
	PCM []byte
}

// FramesOf splits the clip's PCM into back-to-back frames of n samples each
// (per channel). The trailing partial frame is dropped.
func (c *Clip) FramesOf(samplesPerFrame int) [][]byte {
	bytesPerFrame := samplesPerFrame * c.Channels * (c.BitDepth / 8)
	var out [][]byte
	for i := 0; i+bytesPerFrame <= len(c.PCM); i += bytesPerFrame {
		out = append(out, c.PCM[i:i+bytesPerFrame])
	}
	return out
}

// LoadClip resolves and decodes tests/voice-clips/<name>/audio.wav, failing
// the test if the file is missing or malformed.
func LoadClip(t *testing.T, name string) *Clip {
	t.Helper()
	path := filepath.Join(repoRoot(), "tests", "voice-clips", name, "audio.wav")
	c, err := loadWAV(path)
	if err != nil {
		t.Fatalf("voicetest.LoadClip(%q): %v", name, err)
	}
	c.Name = name
	return c
}

// loadWAV parses a minimal RIFF/WAVE PCM file: it locates the fmt and data
// subchunks and ignores everything else.
func loadWAV(path string) (*Clip, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, fmt.Errorf("read RIFF header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a RIFF/WAVE file")
	}

	c := &Clip{}
	var sawFmt, sawData bool
	for !sawData {
		var ck [8]byte
		if _, err := io.ReadFull(f, ck[:]); err != nil {
			return nil, fmt.Errorf("read chunk header: %w", err)
		}
		id := string(ck[0:4])
		size := int64(binary.LittleEndian.Uint32(ck[4:8]))

		switch id {
		case "fmt ":
			buf := make([]byte, size)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, fmt.Errorf("read fmt chunk: %w", err)
			}
			if len(buf) < 16 {
				return nil, fmt.Errorf("fmt chunk too short: %d bytes", len(buf))
			}
			format := binary.LittleEndian.Uint16(buf[0:2])
			if format != 1 {
				return nil, fmt.Errorf("only PCM (format=1) supported, got %d", format)
			}
			c.Channels = int(binary.LittleEndian.Uint16(buf[2:4]))
			c.SampleRate = int(binary.LittleEndian.Uint32(buf[4:8]))
			c.BitDepth = int(binary.LittleEndian.Uint16(buf[14:16]))
			sawFmt = true
		case "data":
			c.PCM = make([]byte, size)
			if _, err := io.ReadFull(f, c.PCM); err != nil {
				return nil, fmt.Errorf("read data chunk: %w", err)
			}
			sawData = true
		default:
			if _, err := f.Seek(size, io.SeekCurrent); err != nil {
				return nil, fmt.Errorf("skip %q chunk: %w", id, err)
			}
		}
	}
	if !sawFmt {
		return nil, fmt.Errorf("missing fmt chunk")
	}
	return c, nil
}

// repoRoot returns the path to the repository root, computed once on first
// call. We walk up from this source file looking for go.mod because tests
// run with cwd=package, so a relative path to tests/voice-clips/ would be
// brittle as the package layout evolves.
//
// Failure here means the test binary was built outside a Go module, which
// is fundamentally broken — panic gives a clean stack trace.
var repoRoot = sync.OnceValue(findRepoRoot)

func findRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("voicetest: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("voicetest: go.mod not found above " + filepath.Dir(file))
		}
		dir = parent
	}
}
