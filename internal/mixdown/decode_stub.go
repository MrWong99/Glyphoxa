//go:build !opus

package mixdown

// The default build carries no Opus decoder, so the default decoder is unavailable —
// [WAVClip] with a nil Options.Decoder and any frame to decode reports
// [ErrDecoderUnavailable]. This mirrors the codec_stub precedent: the tree stays
// green under plain `go test ./...` while the deterministic suite injects its
// own [DecoderFactory]. Build with `-tags opus` for the real decoder
// (decode_opus.go).
func init() {
	defaultDecoderFactory = func() (Decoder, error) { return nil, ErrDecoderUnavailable }
}
