package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestSealOpenRoundTrip(t *testing.T) {
	c := newCipher(t)
	plain := []byte("sk-ant-secret-key-value")

	sealed, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("ciphertext contains plaintext")
	}

	got, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plain)
	}
}

func TestSealIsNondeterministic(t *testing.T) {
	c := newCipher(t)
	plain := []byte("same-input")

	a, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal a: %v", err)
	}
	b, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext were identical (nonce not random)")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	c := newCipher(t)
	sealed, err := c.Seal([]byte("data"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff // flip a tag byte
	if _, err := c.Open(sealed); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	c1 := newCipher(t)
	c2 := newCipher(t)
	sealed, err := c1.Seal([]byte("data"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := c2.Open(sealed); err == nil {
		t.Fatal("Open with wrong key succeeded")
	}
}

func TestNewRejectsBadKeySize(t *testing.T) {
	if _, err := New(make([]byte, 16)); err != ErrKeySize {
		t.Fatalf("16-byte key: got %v want ErrKeySize", err)
	}
}

func TestLast4(t *testing.T) {
	cases := map[string]string{
		"sk-ant-abcd1234": "1234",
		"abc":             "abc",
		"abcd":            "abcd",
		"abcde":           "bcde",
		"":                "",
	}
	for in, want := range cases {
		if got := Last4(in); got != want {
			t.Errorf("Last4(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOpenRejectsUnknownVersion pins the sealed-blob versioning: the leading
// version byte exists so key/algorithm rotation can dispatch on it, which only
// works if unknown versions are rejected loudly instead of fed to the AEAD.
func TestOpenRejectsUnknownVersion(t *testing.T) {
	c := newCipher(t)
	sealed, err := c.Seal([]byte("data"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed[0] = 0x7f
	if _, err := c.Open(sealed); err == nil {
		t.Fatal("Open accepted an unknown sealed-blob version")
	}
}
