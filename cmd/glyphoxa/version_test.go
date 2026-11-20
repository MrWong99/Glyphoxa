package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestStubBuildWarnings(t *testing.T) {
	cases := []struct {
		name        string
		codec, dave bool
		want        []string
	}{
		{"live build", true, true, nil},
		{"stub codec", false, true, []string{"-tags opus"}},
		{"stub dave", true, false, []string{"-tags dave"}},
		{"plain go build", false, false, []string{"-tags opus", "-tags dave"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stubBuildWarnings(tc.codec, tc.dave)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d warnings %q, want %d", len(got), got, len(tc.want))
			}
			for i, frag := range tc.want {
				if !strings.Contains(got[i], frag) {
					t.Errorf("warning %d = %q, want it to name %q", i, got[i], frag)
				}
			}
		})
	}
}

// TestLogBuildInfo_WarnsOnlyForVoiceModes pins that the stub-build WARN fires
// for the Modes that host the voice loop and stays quiet for a web replica.
// The test binary is built without the voice tags, so it IS a stub build.
func TestLogBuildInfo_WarnsOnlyForVoiceModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		warn bool
	}{{"all", true}, {"voice", true}, {"web", false}} {
		var buf bytes.Buffer
		logBuildInfo(slog.New(slog.NewTextHandler(&buf, nil)), tc.mode)
		out := buf.String()
		if !strings.Contains(out, "version=") {
			t.Errorf("mode %s: boot log lacks the version: %s", tc.mode, out)
		}
		if got := strings.Contains(out, "level=WARN"); got != tc.warn {
			t.Errorf("mode %s: WARN logged = %t, want %t:\n%s", tc.mode, got, tc.warn, out)
		}
	}
}

func TestRunVersion(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v9.9.9"
	var buf bytes.Buffer
	runVersion(&buf)
	if got := buf.String(); !strings.HasPrefix(got, "glyphoxa v9.9.9 (") {
		t.Fatalf("runVersion = %q, want the stamped version first", got)
	}
}
