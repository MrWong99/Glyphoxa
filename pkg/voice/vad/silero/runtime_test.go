package silero

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	cases := map[string]bool{
		"libonnxruntime.so":   true,
		"libonnxruntime.so.1": true,
		"":                    false,
		".":                   false,
		"..":                  false,
		"../evil":             false,
		"foo/bar":             false,
		"a..b":                false, // conservative: any ".." substring is rejected
		"/etc/passwd":         false,
	}
	for name, want := range cases {
		if got := safeName(name); got != want {
			t.Errorf("safeName(%q) = %v, want %v", name, got, want)
		}
	}
}

// tarEntry describes one entry to write into a synthetic release tarball.
type tarEntry struct {
	name string
	typ  byte
	body string // for regular files
	link string // for symlinks
}

// writeTarGz writes entries into a gzip-compressed tar at a temp path and
// returns the path, mirroring the shape of a Microsoft ONNX Runtime release
// (a single top-level directory containing lib/, headers, docs).
func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.tgz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tarball: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644}
		switch e.typ {
		case tar.TypeReg:
			hdr.Size = int64(len(e.body))
		case tar.TypeSymlink:
			hdr.Linkname = e.link
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return path
}

// TestExtractLibFromTarball_ExtractsAndFlattensLib proves the happy path: a
// regular file under "<top>/lib/" is written into destLibDir flattened to its
// base name, while a within-lib symlink is recreated and non-lib entries are
// skipped.
func TestExtractLibFromTarball_ExtractsAndFlattensLib(t *testing.T) {
	tgz := writeTarGz(t, []tarEntry{
		{name: "onnxruntime-1.25.1/lib/libonnxruntime.so.1", typ: tar.TypeReg, body: "ELF-bytes"},
		{name: "onnxruntime-1.25.1/lib/libonnxruntime.so", typ: tar.TypeSymlink, link: "libonnxruntime.so.1"},
		{name: "onnxruntime-1.25.1/include/onnxruntime.h", typ: tar.TypeReg, body: "header"},
		{name: "onnxruntime-1.25.1/LICENSE", typ: tar.TypeReg, body: "license"},
	})
	dest := t.TempDir()

	if err := extractLibFromTarball(tgz, dest); err != nil {
		t.Fatalf("extractLibFromTarball: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "libonnxruntime.so.1"))
	if err != nil {
		t.Fatalf("read extracted lib: %v", err)
	}
	if string(got) != "ELF-bytes" {
		t.Errorf("extracted content = %q, want %q", got, "ELF-bytes")
	}

	link, err := os.Readlink(filepath.Join(dest, "libonnxruntime.so"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "libonnxruntime.so.1" {
		t.Errorf("symlink target = %q, want %q", link, "libonnxruntime.so.1")
	}

	// Non-lib entries must not be extracted.
	for _, skipped := range []string{"onnxruntime.h", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dest, skipped)); !os.IsNotExist(err) {
			t.Errorf("non-lib entry %q was extracted (err=%v), want skipped", skipped, err)
		}
	}
}

// TestExtractLibFromTarball_RejectsEscapingSymlink pins the symlink-escape
// guard: a lib symlink whose target tries to leave the destination is rejected
// rather than created.
func TestExtractLibFromTarball_RejectsEscapingSymlink(t *testing.T) {
	for _, target := range []string{"../../etc/passwd", "/etc/passwd"} {
		t.Run(target, func(t *testing.T) {
			tgz := writeTarGz(t, []tarEntry{
				{name: "onnxruntime-1.25.1/lib/evil.so", typ: tar.TypeSymlink, link: target},
			})
			err := extractLibFromTarball(tgz, t.TempDir())
			if err == nil {
				t.Fatalf("escaping symlink target %q was accepted, want rejection", target)
			}
			if !strings.Contains(err.Error(), "unsafe symlink target") {
				t.Errorf("error %q does not name the unsafe symlink target", err)
			}
		})
	}
}

// TestExtractLibFromTarball_FlattensTraversalName proves a lib entry whose path
// contains traversal components cannot escape destLibDir: filepath.Base reduces
// it to a single safe component written inside the destination.
func TestExtractLibFromTarball_FlattensTraversalName(t *testing.T) {
	dest := t.TempDir()
	tgz := writeTarGz(t, []tarEntry{
		{name: "onnxruntime-1.25.1/lib/../../../../tmp/escaped.so", typ: tar.TypeReg, body: "x"},
	})

	if err := extractLibFromTarball(tgz, dest); err != nil {
		t.Fatalf("extractLibFromTarball: %v", err)
	}
	// The entry is flattened to its base name inside dest, never written outside.
	if _, err := os.Stat(filepath.Join(dest, "escaped.so")); err != nil {
		t.Errorf("flattened entry not found in dest: %v", err)
	}
	if _, err := os.Stat("/tmp/escaped.so"); err == nil {
		t.Error("entry escaped to /tmp/escaped.so — traversal not contained")
		os.Remove("/tmp/escaped.so")
	}
}
