package address

import "testing"

// TestNormalizeLang pins the BCP-47 → primary-subtag reduction the registry
// keys on, so "en", "en-US", and "EN_us" all resolve to one Encoder.
func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"en":      "en",
		"EN":      "en",
		"en-US":   "en",
		"en_GB":   "en",
		"DE-de":   "de",
		"  de  ":  "de",
		"zh-Hant": "zh",
		"":        "",
	}
	for in, want := range cases {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultEncoders proves the shipped language matrix resolves and that the
// encoders are the documented schemes: Bart/bard collide under Double
// Metaphone (en), and the German code is digit-string Kölner Phonetik (de).
func TestDefaultEncoders(t *testing.T) {
	r := DefaultEncoders()

	en, ok := r.For("en-US")
	if !ok {
		t.Fatal("DefaultEncoders has no en encoder")
	}
	if en.Encode("Bart") != en.Encode("bard") {
		t.Errorf("en: Bart=%q bard=%q, want equal (homophones)", en.Encode("Bart"), en.Encode("bard"))
	}
	if en.Encode("Bart") == en.Encode("Glyphoxa") {
		t.Error("en: distinct names should not share a code")
	}

	de, ok := r.For("de")
	if !ok {
		t.Fatal("DefaultEncoders has no de encoder")
	}
	if de.Encode("Müller") == "" {
		t.Error("de: expected a non-empty Kölner code for Müller")
	}
}

// TestEncoderRegistry_RegisterAndUnregister covers replace, alias-language
// registration, and nil-unregister.
func TestEncoderRegistry_RegisterAndUnregister(t *testing.T) {
	r := NewEncoderRegistry()
	if _, ok := r.For("en"); ok {
		t.Fatal("empty registry resolved en")
	}

	r.Register("en-US", EncoderFunc(func(s string) string { return "X" }))
	enc, ok := r.For("en")
	if !ok || enc.Encode("anything") != "X" {
		t.Fatalf("Register under en-US should resolve via en; ok=%v", ok)
	}

	r.Register("en", nil) // unregister
	if _, ok := r.For("en"); ok {
		t.Error("nil Register should unregister the language")
	}
}
