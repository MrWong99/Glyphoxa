package bundle

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// FormatVersion is the current campaign bundle format version (ADR-0053 §7).
//
// v2 (#547) added Aspects and Tags on a Node, Note/Disposition on an Edge, and
// the Maps, Pins, Boards and Appearances sections. Every one of them is an
// OMITEMPTY addition, so a v1 bundle is a valid v2 bundle with those sections
// absent — which is why [CheckVersion] accepts v1 rather than rejecting it. A
// backup format that refuses last year's backup is not a backup format.
//
// v3 (#592, ADR-0062) added the planning_threads section — the Butler planning
// chat's conversations, prose only. Same omitempty discipline: a v1 or v2
// bundle is a valid v3 bundle with the section absent.
const FormatVersion = 3

// MinSupportedVersion is the oldest format this build can import.
const MinSupportedVersion = 1

// MaxDecodedBytes caps the decompressed JSON size Decode will read, guarding
// against gzip bombs.
const MaxDecodedBytes int64 = 256 << 20 // 256 MiB

// MaxImportBytes caps the COMPRESSED bundle an import will accept.
//
// It is deliberately larger than blob.MaxSize (#547). That constant is the cap on
// ONE image; an images-included bundle carries several, base64-inflated by a
// third and only partly recovered by gzip. Capping the whole upload at the
// per-image limit made an export this build can PRODUCE impossible to import
// through the only import path — a backup that cannot be restored. This is the
// binding bound on bundle size and it is documented as such in ADR-0053.
const MaxImportBytes int64 = 192 << 20 // 192 MiB compressed

// importLimit is the effective upload cap; a var so a test can shrink it rather
// than allocate 192 MiB to prove the guard fires. Mirrors decodeLimit.
var importLimit = MaxImportBytes

// SetImportLimitForTest shrinks the upload cap for the duration of a test.
func SetImportLimitForTest(n int64) func() {
	prev := importLimit
	importLimit = n
	return func() { importLimit = prev }
}

// decodeLimit is the effective decode cap; a var so tests can shrink it.
var decodeLimit = MaxDecodedBytes

// inflateFloor and maxInflateRatio are the gzip-bomb guard on top of the flat
// decode cap. A gzipped bundle may always decode to inflateFloor bytes; past
// that, the decoded size may not exceed maxInflateRatio times the compressed
// bytes actually read so far. An honest bundle stays well under it — base64 map
// images inflate ~1.3×, indented JSON prose 5–10× — while a run of one repeated
// byte inflates ~1000×, so a 250 KiB bomb is refused after 32 MiB of inflating
// instead of after the whole 256 MiB cap.
const (
	inflateFloor    int64 = 32 << 20
	maxInflateRatio int64 = 20
)

// importConcurrency bounds how many bundle transfers — imports AND exports
// (ServeExport shares this gate) — may run at once. The per-request caps
// (192 MiB compressed, 256 MiB decoded, the inflate ratio — ADR-0053) bound one
// request; this bounds how many hold that much memory together. Both directions
// build the whole Bundle in memory, and the web pod's memory limit has room for
// one of those, not two. Bundles are rare and operator-driven; serializing them
// costs nothing.
const importConcurrency = 1

var importGate = make(chan struct{}, importConcurrency)

// acquireImportSlot takes a bundle slot (shared by import and export) without
// blocking; ok is false when every slot is busy, and release returns the slot.
func acquireImportSlot() (release func(), ok bool) {
	select {
	case importGate <- struct{}{}:
		return func() { <-importGate }, true
	default:
		return nil, false
	}
}

// HoldImportSlotsForTest fills every import slot until the returned release is
// called, so a test can prove the gate answers 503 without racing a real import.
func HoldImportSlotsForTest() (release func()) {
	for range cap(importGate) {
		importGate <- struct{}{}
	}
	return func() {
		for range cap(importGate) {
			<-importGate
		}
	}
}

var (
	// ErrNewerFormat means the bundle was written by a newer build than this one.
	ErrNewerFormat = errors.New("bundle format is newer than this build supports")
	// ErrUnsupportedFormat means the format version has no migration path here.
	ErrUnsupportedFormat = errors.New("bundle format version is unsupported")
	// ErrInvalidBundle wraps every importer-side validation failure of the
	// bundle's own content (a dangling ref, a duplicate ref, a bad image) — a
	// client-caused 400, never a 500 with an alarming Error log.
	ErrInvalidBundle = errors.New("invalid campaign bundle")
	// ErrTooLarge means the decompressed bundle exceeded the decode cap or
	// inflated past the gzip-bomb ratio guard.
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
// JSON stream. It enforces the decode-size cap and, for gzip, the inflate-ratio
// guard; rejects unknown fields; and checks the format version.
//
// The document is decoded STREAMING (the gzip-bomb fix): the inflated bytes are
// never collected into one buffer first, and the version gate is not a second
// full Unmarshal. Instead the top-level object is walked key by key, so
// format_version — which [Encode] always writes first — is checked before the
// campaign section is decoded strictly. A newer bundle therefore still gets the
// "format is newer" error (ADR-0053 §7), not a cryptic unknown-field one. A
// hand-written bundle that puts format_version AFTER a section this build
// cannot parse gets the parse error instead.
func Decode(r io.Reader) (*Bundle, error) {
	compressed := &countingReader{r: r}
	br := bufio.NewReader(compressed)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		return nil, err
	}

	guard := &inflateGuard{r: br}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		guard = &inflateGuard{r: gz, compressed: compressed}
	}

	dec := json.NewDecoder(guard)
	dec.DisallowUnknownFields()
	b, err := decodeBundle(dec)
	if err != nil {
		// The guard's refusal reaches the decoder as a read error; return the
		// guard's own so ErrTooLarge survives however the decoder reported it.
		if guard.err != nil {
			return nil, guard.err
		}
		return nil, err
	}
	return b, nil
}

// decodeBundle walks a bundle's top-level object, gating on format_version the
// moment it is read (see [Decode]).
func decodeBundle(dec *json.Decoder) (*Bundle, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	var b Bundle
	versionSeen := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		switch key {
		case "format_version":
			if err := dec.Decode(&b.FormatVersion); err != nil {
				return nil, err
			}
			if err := checkVersionMsg(b.FormatVersion); err != nil {
				return nil, err
			}
			versionSeen = true
		case "exported_at":
			if err := dec.Decode(&b.ExportedAt); err != nil {
				return nil, err
			}
		case "campaign":
			if err := dec.Decode(&b.Campaign); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("json: unknown field %q", key)
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, err
	}
	if !versionSeen {
		// No format_version reads as version 0: unsupported, with the same
		// message naming both versions.
		if err := checkVersionMsg(0); err != nil {
			return nil, err
		}
	}
	return &b, nil
}

// expectDelim consumes the next token and fails unless it is want.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("json: bundle is not a JSON object (got %v)", tok)
	}
	return nil
}

// checkVersionMsg is [CheckVersion] wrapped in the client-facing message that
// names both versions (ADR-0053 §7).
func checkVersionMsg(v int) error {
	if err := CheckVersion(v); err != nil {
		return fmt.Errorf("bundle has format_version %d; this build supports %d: %w", v, FormatVersion, err)
	}
	return nil
}

// countingReader counts the bytes read through it: the compressed side of the
// inflate-ratio guard. It counts what the gzip reader's buffer has pulled, at
// most a buffer ahead of what was inflated — slack that only errs towards
// accepting.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// inflateGuard counts decoded bytes and fails, stickily, once they pass the
// decode cap or — for a gzipped stream (compressed != nil) — once they pass
// both inflateFloor and maxInflateRatio times the compressed bytes read so far.
// It fails while the bytes ARRIVE, so a bomb costs at most about inflateFloor of
// inflating, never the whole decode cap.
type inflateGuard struct {
	r          io.Reader
	compressed *countingReader
	decoded    int64
	err        error
}

func (g *inflateGuard) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n, err := g.r.Read(p)
	g.decoded += int64(n)
	switch {
	case g.decoded > decodeLimit:
		g.err = fmt.Errorf("%w (%d MiB)", ErrTooLarge, decodeLimit>>20)
	case g.compressed != nil && g.decoded > inflateFloor && g.decoded > maxInflateRatio*g.compressed.n:
		g.err = fmt.Errorf("%w: it inflates more than %d times its compressed size", ErrTooLarge, maxInflateRatio)
	}
	if g.err != nil {
		return 0, g.err
	}
	return n, err
}

// CheckVersion reports whether v is a supported bundle format version.
func CheckVersion(v int) error {
	switch {
	case v >= MinSupportedVersion && v <= FormatVersion:
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
