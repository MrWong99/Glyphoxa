package silero

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// onnxRuntimeVersion pins the ONNX Runtime release we resolve at runtime.
// Bumping this requires updating the per-arch SHA-256 sums below.
const onnxRuntimeVersion = "1.25.1"

// onnxLibName is the shared library file we extract from the release tarball.
const onnxLibName = "libonnxruntime.so"

// onnxArtifact describes a per-platform release artifact.
type onnxArtifact struct {
	url    string
	sha256 string
}

// onnxArtifacts maps GOOS/GOARCH pairs to the official Microsoft release
// tarball for that platform. Only Linux x64 and aarch64 are supported today;
// other platforms return an error from ensureRuntime.
var onnxArtifacts = map[string]onnxArtifact{
	"linux/amd64": {
		url:    "https://github.com/microsoft/onnxruntime/releases/download/v" + onnxRuntimeVersion + "/onnxruntime-linux-x64-" + onnxRuntimeVersion + ".tgz",
		sha256: "eb566a49cfc49ef0642f809b69340b5bb656c7c4905ba873526d226f2c005816",
	},
	"linux/arm64": {
		url:    "https://github.com/microsoft/onnxruntime/releases/download/v" + onnxRuntimeVersion + "/onnxruntime-linux-aarch64-" + onnxRuntimeVersion + ".tgz",
		sha256: "daa71b56b00c4ab34798a3d96ca41a32ece4d3e302dc2386d3cca83fd4491214",
	},
}

// runtimeMu serialises concurrent ensureRuntime calls within one process so
// two callers don't race on the cache-population step.
var runtimeMu sync.Mutex

// ensureRuntime returns a path to libonnxruntime.so, populating the per-user
// cache directory on first use. The resolution order is:
//
//  1. The GLYPHOXA_ONNX_LIB environment variable, if set.
//  2. <UserCacheDir>/glyphoxa/onnxruntime/<version>/lib/libonnxruntime.so,
//     if present.
//  3. The official Microsoft release tarball for GOOS/GOARCH, downloaded and
//     extracted into the cache (with SHA-256 verification).
//
// Currently only linux/amd64 and linux/arm64 are supported.
func ensureRuntime() (string, error) {
	if p := os.Getenv("GLYPHOXA_ONNX_LIB"); p != "" {
		return p, nil
	}

	platform := runtime.GOOS + "/" + runtime.GOARCH
	art, ok := onnxArtifacts[platform]
	if !ok {
		return "", fmt.Errorf("silero: ONNX Runtime auto-install not supported on %s; set GLYPHOXA_ONNX_LIB to the path of libonnxruntime.so", platform)
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("silero: locate user cache dir: %w", err)
	}
	libDir := filepath.Join(cacheRoot, "glyphoxa", "onnxruntime", onnxRuntimeVersion, "lib")
	libPath := filepath.Join(libDir, onnxLibName)

	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if _, err := os.Stat(libPath); err == nil {
		return libPath, nil
	}

	if err := downloadAndExtract(art, libDir); err != nil {
		return "", fmt.Errorf("silero: install ONNX Runtime %s: %w", onnxRuntimeVersion, err)
	}
	return libPath, nil
}

// downloadAndExtract fetches the release tarball, verifies its SHA-256 against
// the pinned hash, and extracts every entry under "lib/" into destLibDir.
// Other entries (headers, docs, license) are skipped — only the runtime is
// needed at execution time.
func downloadAndExtract(artifact onnxArtifact, destLibDir string) error {
	if err := os.MkdirAll(destLibDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	resp, err := http.Get(artifact.url)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", artifact.url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "onnxruntime-*.tgz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("write tarball: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tarball: %w", err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != artifact.sha256 {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", artifact.url, got, artifact.sha256)
	}

	return extractLibFromTarball(tmpPath, destLibDir)
}

// extractLibFromTarball reads tarballPath (gzip-compressed tar) and writes
// every regular file whose path begins with "<topdir>/lib/" into destLibDir,
// flattening the directory layout. The Microsoft release tarballs always have
// a single top-level directory whose name matches the artifact stem.
func extractLibFromTarball(tarballPath, destLibDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeSymlink {
			continue
		}
		// Strip the top-level directory and require a "lib/" prefix on what remains.
		parts := strings.SplitN(header.Name, "/", 2)
		if len(parts) != 2 {
			continue
		}
		rel := parts[1]
		if !strings.HasPrefix(rel, "lib/") {
			continue
		}
		base := filepath.Base(rel)
		if !safeName(base) {
			return fmt.Errorf("unsafe entry name %q in tarball", header.Name)
		}
		out := filepath.Join(destLibDir, base)

		switch header.Typeflag {
		case tar.TypeReg:
			if err := writeRegularEntry(tr, out, header.Mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// The release tarball's symlinks point within the lib/ directory
			// (e.g. libonnxruntime.so → libonnxruntime.so.1). Reject anything
			// that tries to escape.
			if !safeName(header.Linkname) {
				return fmt.Errorf("unsafe symlink target %q in tarball", header.Linkname)
			}
			_ = os.Remove(out) // os.Symlink fails if the target exists
			if err := os.Symlink(header.Linkname, out); err != nil {
				return fmt.Errorf("symlink %s → %s: %w", out, header.Linkname, err)
			}
		}
	}
}

// safeName returns true iff name is a single path component with no traversal.
func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		return false
	}
	return true
}

// writeRegularEntry copies the current tar entry into destPath with the given mode.
func writeRegularEntry(tr *tar.Reader, destPath string, mode int64) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode)&0o777)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}
	if _, err := io.Copy(out, tr); err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", destPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", destPath, err)
	}
	return nil
}
