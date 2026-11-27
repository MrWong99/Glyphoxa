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

// workflow mirrors just enough of the GitHub Actions schema to inspect steps
// and timeouts.
type workflow struct {
	Jobs map[string]struct {
		TimeoutMinutes int    `yaml:"timeout-minutes"`
		Steps          []step `yaml:"steps"`
	} `yaml:"jobs"`
}

type step struct {
	Name           string         `yaml:"name"`
	Uses           string         `yaml:"uses"`
	Run            string         `yaml:"run"`
	With           map[string]any `yaml:"with"`
	TimeoutMinutes int            `yaml:"timeout-minutes"`
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
	eachDockerBuildJob(t, func(t *testing.T, file, jobName string, steps []step, build int) {
		if !provisionsGen(steps[:build]) {
			t.Errorf(
				"%s: job %q runs docker/build-push-action (step %d) without an earlier step providing gen/ "+
					"(`buf generate` or download-artifact `gen`); the Dockerfile's `COPY . .` + `go build` "+
					"cannot compile without the gitignored stubs in the context",
				file, jobName, build,
			)
		}
	})
}

// TestDockerBuildJobsEmbedRealSPA enforces the build-context contract the #114
// ADR-0034 amendment adds ("The SPA bundle is context-fed, not built in the
// image"): every workflow job that runs a docker build of this repo must, in an
// earlier step of the same job, provision the REAL built SPA bundle into
// internal/spa/dist — either by downloading the `spa-dist` artifact (ci.yml's
// `web` job builds and uploads it) or by building it in-job (release-image.yml's
// publish can't cross-workflow the artifact, so it runs `npm run build` itself,
// exactly as it self-generates gen/).
//
// Like the gen invariant, this is cross-file and latent: without it a docker
// build silently embeds the committed placeholder index.html instead of the
// console, and the release path only runs on a v* tag — long after the PR that
// broke it merged.
func TestDockerBuildJobsEmbedRealSPA(t *testing.T) {
	eachDockerBuildJob(t, func(t *testing.T, file, jobName string, steps []step, build int) {
		if !provisionsRealSPA(steps[:build]) {
			t.Errorf(
				"%s: job %q runs docker/build-push-action (step %d) without an earlier step providing the real "+
					"SPA bundle (download-artifact `spa-dist` or `npm run build`); the Dockerfile's `COPY . .` "+
					"would then embed the committed placeholder internal/spa/dist/index.html, not the console",
				file, jobName, build,
			)
		}
	})
}

// maxJobTimeoutMinutes is the ceiling for a job's timeout-minutes. The slowest
// job (integration) runs its go test with -timeout 20m; an hour leaves room
// for setup without letting a hang run for most of GitHub's 360-minute default.
const maxJobTimeoutMinutes = 60

// TestEveryJobDeclaresATimeout enforces that every job in every workflow bounds
// itself with timeout-minutes. Without one a job falls back to GitHub's
// 360-minute default, and a hung step does more than waste runner time: on
// 2026-11-20 the e2e job's "Delete k3d cluster" step hung for 90 minutes after
// a failed smoke test and hid the failure (#693).
func TestEveryJobDeclaresATimeout(t *testing.T) {
	eachWorkflow(t, func(file string, wf workflow) {
		for jobName, job := range wf.Jobs {
			if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > maxJobTimeoutMinutes {
				t.Errorf(
					"%s: job %q has timeout-minutes %d, want 1..%d; an unbounded job runs up to GitHub's "+
						"360-minute default, and a hung job hides the real failure behind it (#693)",
					file, jobName, job.TimeoutMinutes, maxJobTimeoutMinutes,
				)
			}
		}
	})
}

// TestK3dTeardownStepsAreBounded enforces that every step deleting a k3d
// cluster carries its own short step-level timeout-minutes. Teardown runs under
// `if: always()` after the smoke test, so it is the step that hung in #693; the
// job-level bound alone would still leave the failed smoke result waiting
// behind it for the rest of the job's budget.
func TestK3dTeardownStepsAreBounded(t *testing.T) {
	found := false
	eachWorkflow(t, func(file string, wf workflow) {
		for jobName, job := range wf.Jobs {
			for _, s := range job.Steps {
				if !strings.Contains(s.Run, "k3d cluster delete") {
					continue
				}
				found = true
				if s.TimeoutMinutes < 1 || s.TimeoutMinutes > 10 {
					t.Errorf(
						"%s: job %q step %q deletes a k3d cluster with step timeout-minutes %d, want 1..10; "+
							"a wedged teardown must be cut quickly so the job reports the failed smoke test (#693)",
						file, jobName, s.Name, s.TimeoutMinutes,
					)
				}
			}
		}
	})
	if !found {
		t.Error("no `k3d cluster delete` step found in any workflow — did the e2e teardown move or get renamed?")
	}
}

// eachDockerBuildJob parses every workflow file and invokes fn for each job that
// runs a docker/build-push-action step, passing that step's index so callers can
// assert on the steps that precede it.
func eachDockerBuildJob(t *testing.T, fn func(t *testing.T, file, jobName string, steps []step, build int)) {
	t.Helper()
	eachWorkflow(t, func(file string, wf workflow) {
		for jobName, job := range wf.Jobs {
			build := dockerBuildIndex(job.Steps)
			if build < 0 {
				continue
			}
			fn(t, file, jobName, job.Steps, build)
		}
	})
}

// eachWorkflow parses every workflow file and invokes fn with its base name and
// parsed contents.
func eachWorkflow(t *testing.T, fn func(file string, wf workflow)) {
	t.Helper()
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
		fn(filepath.Base(file), wf)
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

// provisionsRealSPA reports whether any of the given steps puts the real built
// SPA bundle into the working tree (internal/spa/dist): building it in-job with
// `npm run build`, or restoring the `spa-dist` artifact uploaded by ci.yml's
// `web` job.
func provisionsRealSPA(steps []step) bool {
	for _, s := range steps {
		if strings.Contains(s.Run, "npm run build") {
			return true
		}
		if strings.HasPrefix(s.Uses, "actions/download-artifact") {
			if name, ok := s.With["name"].(string); ok && name == "spa-dist" {
				return true
			}
		}
	}
	return false
}
