package ci

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// minSafeRelease is the oldest Glyphoxa release that may be referenced as a
// deployable image anywhere in the repo. Releases below v0.16.0 carry the
// type-20-channel IDENTIFY-loop defect that burned production's gateway budget;
// they must never be run again. Raise it deliberately when a newer release
// becomes the floor.
const minSafeRelease = "v0.16.0"

// repoRoot is the module root relative to this package.
var repoRoot = filepath.Join("..", "..")

// TestReleaseImageGatesBeforePush pins the two publish gates in
// release-image.yml: before any docker/build-push-action step that pushes, the
// job must (1) check that ci.yml passed for the tagged commit and (2) run
// scripts/container-smoke.sh against a locally built image. Like the gen/SPA
// invariants this is latent — the workflow only runs on a v* tag, long after
// the PR that removed a gate merged.
func TestReleaseImageGatesBeforePush(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release-image.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	pushes := 0
	for jobName, job := range wf.Jobs {
		for i, s := range job.Steps {
			if !strings.HasPrefix(s.Uses, "docker/build-push-action") || s.With["push"] != true {
				continue
			}
			pushes++
			var ciGreen, smoked bool
			for _, prev := range job.Steps[:i] {
				if strings.Contains(prev.Run, "workflows/ci.yml/runs") && strings.Contains(prev.Run, "status=success") {
					ciGreen = true
				}
				if strings.Contains(prev.Run, "scripts/container-smoke.sh") {
					smoked = true
				}
			}
			if !ciGreen {
				t.Errorf("release-image.yml job %q pushes (step %d) without first requiring a green ci.yml run for the commit", jobName, i)
			}
			if !smoked {
				t.Errorf("release-image.yml job %q pushes (step %d) without first running scripts/container-smoke.sh", jobName, i)
			}
		}
	}
	if pushes == 0 {
		t.Fatal("release-image.yml has no pushing docker/build-push-action step — test is looking at the wrong file?")
	}
}

// TestComposeImageTagOverridable pins compose.yml's Glyphoxa image to an
// env-overridable tag. A literal version here rotted at v0.2.0 for fourteen
// releases (the self-host on-ramp pulled an image with known-fatal defects).
func TestComposeImageTagOverridable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	img := c.Services["glyphoxa"].Image
	const want = "ghcr.io/mrwong99/glyphoxa:${GLYPHOXA_IMAGE_TAG:-"
	if !strings.HasPrefix(img, want) {
		t.Fatalf("compose.yml glyphoxa image = %q, want it to start with %q (overridable tag)", img, want)
	}
	def := strings.TrimSuffix(strings.TrimPrefix(img, want), "}")
	if def != "latest" && !atLeast(t, def, minSafeRelease) {
		t.Errorf("compose.yml default image tag %q is below the safe floor %s", def, minSafeRelease)
	}
}

// pinPattern matches a literal Glyphoxa release reference: an image ref
// (ghcr.io/mrwong99/glyphoxa:v1.2.3) or a values-style `tag: v1.2.3`.
var pinPattern = regexp.MustCompile(`(?:ghcr\.io/mrwong99/glyphoxa:|\btag:\s*"?)(v\d+\.\d+\.\d+)`)

// TestNoImagePinBelowSafeFloor scans the deploy surface (compose.yml, the Helm
// chart, the SaaS scripts and every Markdown doc outside the ADR history) for a
// literal Glyphoxa release pin older than minSafeRelease, and checks the chart's
// appVersion — the tag the chart deploys when image.tag is unset — is a real,
// v-prefixed release tag at or above the floor.
func TestNoImagePinBelowSafeFloor(t *testing.T) {
	var files []string
	for _, f := range []string{"compose.yml", "README.md", "CONTRIBUTING.md"} {
		files = append(files, filepath.Join(repoRoot, f))
	}
	for _, dir := range []string{"docs", filepath.Join("deploy", "charts"), filepath.Join("deploy", "saas")} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "adr" { // ADRs are dated history, not instructions
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(p) {
			case ".md", ".yaml", ".yml", ".sh":
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pinPattern.FindAllStringSubmatch(string(raw), -1) {
			if !atLeast(t, m[1], minSafeRelease) {
				t.Errorf("%s pins Glyphoxa %s, below the safe floor %s", f, m[1], minSafeRelease)
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "charts", "glyphoxa", "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		AppVersion string `yaml:"appVersion"`
	}
	if err := yaml.Unmarshal(raw, &chart); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(chart.AppVersion, "v") {
		t.Errorf("Chart.yaml appVersion %q must be v-prefixed: it is the fallback image tag and release tags are v-prefixed", chart.AppVersion)
	} else if !atLeast(t, chart.AppVersion, minSafeRelease) {
		t.Errorf("Chart.yaml appVersion %s is below the safe floor %s", chart.AppVersion, minSafeRelease)
	}
}

// atLeast reports whether release tag a (vX.Y.Z, optional -suffix ignored) is
// >= b.
func atLeast(t *testing.T, a, b string) bool {
	t.Helper()
	pa, pb := semver(t, a), semver(t, b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return true
}

func semver(t *testing.T, v string) [3]int {
	t.Helper()
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		t.Fatalf("%q is not a vX.Y.Z release tag", v)
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("%q is not a vX.Y.Z release tag", v)
		}
		out[i] = n
	}
	return out
}
