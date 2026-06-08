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

// TestCorpus_CassetteFilesExist pins that every cassette name a clip declares
// resolves to a tests/voice-cassettes/<name>.yaml — so a renamed/missing
// cassette fails here, not deep in a bench run. Clips marked Cassette() must
// have all three; a partially-backed clip (e.g. bart-test, STT only) is allowed
// but its declared names must still exist.
func TestCorpus_CassetteFilesExist(t *testing.T) {
	root := cassetteRoot(t)
	for _, c := range voicebench.Corpus {
		for _, name := range []string{c.STTCassette, c.TTSCassette, c.LLMCassette} {
			if name == "" {
				continue
			}
			p := filepath.Join(root, name+".yaml")
			if _, err := os.Stat(p); err != nil {
				t.Errorf("corpus clip %q cassette %q: %v", c.Dir, name, err)
			}
		}
	}
	// At least one clip must be fully cassette-backed, else the keyless tier has
	// nothing to drive.
	var anyComplete bool
	for _, c := range voicebench.Corpus {
		anyComplete = anyComplete || c.Cassette()
	}
	if !anyComplete {
		t.Error("no cassette-complete clip in Corpus; keyless tier cannot run")
	}
}

// clipsRoot walks up from the package dir to find tests/voice-clips/.
func clipsRoot(t *testing.T) string { return repoSubdir(t, filepath.Join("tests", "voice-clips")) }

// cassetteRoot walks up to find tests/voice-cassettes/.
func cassetteRoot(t *testing.T) string {
	return repoSubdir(t, filepath.Join("tests", "voice-cassettes"))
}

// repoSubdir walks up from the package dir to find a repo-relative subdir.
func repoSubdir(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, rel)
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
		dir = filepath.Dir(dir)
	}
	t.Skipf("%s not found from package dir; skipping existence check", rel)
	return ""
}
