//go:build record

package voicecassette

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appendProvenance returns notes with a dated "Re-recorded against ElevenLabs
// <model> on <date>." line appended for reviewer context. The append is
// idempotent within a day: re-running -tags=record twice on the same date
// must not accrete duplicate stamps (the recorder loads the existing notes,
// which on the second run already carry the line). Re-records on a later date
// still append, preserving the refresh history.
func appendProvenance(notes, model string) string {
	line := fmt.Sprintf("Re-recorded against ElevenLabs %s on %s.",
		model, time.Now().UTC().Format("2006-01-02"))
	switch {
	case notes == "":
		return line
	case strings.Contains(notes, line):
		return notes
	default:
		return notes + "\n\n" + line
	}
}

// leadingComment returns the cassette file's leading block of YAML comments —
// the contiguous run of blank or "#" lines before the first key — with their
// newlines intact, or "" if the file is absent or starts with content.
//
// yaml.Marshal silently drops comments, so the record path re-prepends this
// block when rewriting a cassette; without it every -tags=record run strips
// the hand-authored header that explains what the cassette pins and how to
// refresh it. The file is read at write time, before the recorder overwrites
// it, so the block reflects whatever a human last committed.
func leadingComment(name string) string {
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" { // trailing piece after the final newline
			break
		}
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}
