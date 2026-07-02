// Package ci holds repository-level invariant tests for the GitHub Actions
// workflows — cross-file contracts that no single workflow's own run can
// enforce, because the violating workflow only executes long after the PR that
// broke it is merged (e.g. release-image.yml runs on the first v* tag).
package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// workflow mirrors just enough of the GitHub Actions schema to inspect steps.
type workflow struct {
	Jobs map[string]struct {
		Steps []step `yaml:"steps"`
	} `yaml:"jobs"`
}

type step struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

// TestDockerBuildJobsProvisionGeneratedStubs enforces the build-context
// contract stated in Dockerfile ("The generated protobuf/Connect stubs (gen/,
// gitignored, ADR-0039) are expected to ALREADY exist in the build context"):
// every workflow job that runs a docker build of this repo must, in an earlier
// step of the same job, either run `buf generate` or download the `gen`
// artifact produced by ci.yml's proto job.
//
// The invariant is cross-file and latent — CI stays green when it breaks,
// because the broken job (release-image.yml's publish) only runs on a release
// tag. That is exactly how #75 shipped the regression reported in #139.
func TestDockerBuildJobsProvisionGeneratedStubs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yamls, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, yamls...)
	if len(files) == 0 {
		t.Fatal("no workflow files found — glob path wrong?")
	}

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var wf workflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s: %v", file, err)
		}

		for jobName, job := range wf.Jobs {
			build := dockerBuildIndex(job.Steps)
			if build < 0 {
				continue
			}
			if !provisionsGen(job.Steps[:build]) {
				t.Errorf(
					"%s: job %q runs docker/build-push-action (step %d) without an earlier step providing gen/ "+
						"(`buf generate` or download-artifact `gen`); the Dockerfile's `COPY . .` + `go build` "+
						"cannot compile without the gitignored stubs in the context",
					filepath.Base(file), jobName, build,
				)
			}
		}
	}
}

// dockerBuildIndex returns the index of the first docker build step, -1 if none.
func dockerBuildIndex(steps []step) int {
	for i, s := range steps {
		if strings.HasPrefix(s.Uses, "docker/build-push-action") {
			return i
		}
	}
	return -1
}

// provisionsGen reports whether any of the given steps puts the generated
// stubs into the working tree: running buf generate, or restoring the `gen`
// artifact uploaded by ci.yml's proto job.
func provisionsGen(steps []step) bool {
	for _, s := range steps {
		if strings.Contains(s.Run, "buf generate") {
			return true
		}
		if strings.HasPrefix(s.Uses, "actions/download-artifact") {
			if name, ok := s.With["name"].(string); ok && name == "gen" {
				return true
			}
		}
	}
	return false
}
