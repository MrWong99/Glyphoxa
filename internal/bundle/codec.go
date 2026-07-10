package bundle

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// FormatVersion is the current campaign bundle format version (ADR-0053 §7).
const FormatVersion = 1

// MaxDecodedBytes caps the decompressed JSON size Decode will read, guarding
// against gzip bombs.
const MaxDecodedBytes int64 = 256 << 20 // 256 MiB

// decodeLimit is the effective decode cap; a var so tests can shrink it.
var decodeLimit = MaxDecodedBytes

var (
	// ErrNewerFormat means the bundle was written by a newer build than this one.
	ErrNewerFormat = errors.New("bundle format is newer than this build supports")
	// ErrUnsupportedFormat means the format version has no migration path here.
	ErrUnsupportedFormat = errors.New("bundle format version is unsupported")
	// ErrTooLarge means the decompressed bundle exceeded the decode cap.
	ErrTooLarge = errors.New("bundle exceeds maximum decoded size")
)

// Encode writes b as gzip-wrapped, indented JSON.
func Encode(w io.Writer, b *Bundle) error {
	gz := gzip.NewWriter(w)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if _, err := gz.Write(data); err != nil {
		return err
	}
	return gz.Close()
}

// Decode reads a bundle, transparently accepting either a gzipped or a plain
// JSON stream. It enforces the decode-size cap, rejects unknown fields, and
// checks the format version.
func Decode(r io.Reader) (*Bundle, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		return nil, err
	}

	var src io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		src = gz
	}

	limited := io.LimitReader(src, decodeLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > decodeLimit {
		return nil, ErrTooLarge
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b Bundle
	if err := dec.Decode(&b); err != nil {
		return nil, err
	}

	if err := CheckVersion(b.FormatVersion); err != nil {
		return nil, fmt.Errorf("bundle has format_version %d; this build supports %d: %w",
			b.FormatVersion, FormatVersion, err)
	}
	return &b, nil
}

// CheckVersion reports whether v is a supported bundle format version.
func CheckVersion(v int) error {
	switch {
	case v == FormatVersion:
		return nil
	case v > FormatVersion:
		return ErrNewerFormat
	default:
		return ErrUnsupportedFormat
	}
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Filename returns the canonical bundle filename for a campaign name:
// "<slug>.glyphoxa.json.gz", where slug is lowercased with runs of non
// [a-z0-9] collapsed to "-" and trimmed; an empty slug becomes "campaign".
func Filename(campaignName string) string {
	slug := slugNonAlnum.ReplaceAllString(strings.ToLower(campaignName), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "campaign"
	}
	return slug + ".glyphoxa.json.gz"
}
