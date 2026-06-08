package voicebench_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voicebench"
)

// TestClipsFor_TierFilter pins the tier selection the harness drives runs with:
// no tiers = whole corpus; a tier filter = just that tier, in manifest order.
func TestClipsFor_TierFilter(t *testing.T) {
	if all := voicebench.ClipsFor(); len(all) != len(voicebench.Corpus) {
		t.Errorf("ClipsFor() returned %d clips, want whole corpus (%d)", len(all), len(voicebench.Corpus))
	}
	dice := voicebench.ClipsFor(voicebench.TierDice)
	if len(dice) == 0 {
		t.Fatal("ClipsFor(TierDice) returned no clips")
	}
	for _, c := range dice {
		if c.Tier != voicebench.TierDice {
			t.Errorf("ClipsFor(TierDice) returned %s in tier %q", c.Dir, c.Tier)
		}
	}
}

// TestCorpus_ClipDirsExist pins that every manifest entry points at a real clip
// directory with an audio.wav under tests/voice-clips/ — so a renamed/removed
// clip fails the keyless suite here instead of at bench-run time. Locates the
// repo's tests/voice-clips/ by walking up from the test's working dir.
func TestCorpus_ClipDirsExist(t *testing.T) {
	root := clipsRoot(t)
	for _, c := range voicebench.Corpus {
		wav := filepath.Join(root, c.Dir, "audio.wav")
		if _, err := os.Stat(wav); err != nil {
			t.Errorf("corpus clip %q: %v", c.Dir, err)
		}
	}
}

// clipsRoot walks up from the package dir to find tests/voice-clips/.
func clipsRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "tests", "voice-clips")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("tests/voice-clips not found from package dir; skipping clip-existence check")
	return ""
}
