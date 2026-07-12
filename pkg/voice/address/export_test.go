package address

// Test-only exports (compiled solely under `go test`) that let the external
// address_test package pin unexported internals — here the two tokenizers, so the
// Vocative Flag property test can assert the utterance-path offset tokenizer
// yields a token TEXT sequence byte-identical to the name-path tokenizer.

// Tokenize exposes the internal name-path tokenizer for tests.
func Tokenize(s string) []string { return tokenize(s) }

// OffsetTokenizeTexts exposes the text sequence of the utterance-path
// offset-preserving tokenizer for tests (offsets and gap markers dropped).
func OffsetTokenizeTexts(s string) []string {
	toks, _ := offsetTokenize(s)
	out := make([]string, len(toks))
	for i := range toks {
		out[i] = toks[i].text
	}
	return out
}
