// Package crypto encrypts BYOK Provider Config credentials at rest (ADR-0004):
// AES-256-GCM with a single app-level secret supplied via env var. Credentials
// are write-only after save — only the last 4 characters are kept in plaintext
// for display — so the storage layer never reads a credential back except to
// hand it to the Provider that needs it.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ErrKeySize is returned when the secret is not a valid AES-256 key.
var ErrKeySize = errors.New("crypto: secret must be exactly 32 bytes (AES-256)")

// Cipher seals and opens credential blobs. The nonce is prepended to the
// ciphertext, so a sealed value is self-contained: nonce || ciphertext || tag.
type Cipher struct {
	aead cipher.AEAD
}

// New builds a Cipher from a 32-byte AES-256 key (the decoded app secret).
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext suitable for the
// provider_config.credentials_ciphertext bytea column.
func (c *Cipher) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	// Seal appends the ciphertext+tag to nonce, so the nonce is the prefix.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (c *Cipher) Open(sealed []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("crypto: sealed value too short")
	}
	nonce, ciphertext := sealed[:ns], sealed[ns:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}

// Last4 returns the last 4 characters of a credential for display, the only
// plaintext kept after save (ADR-0004). Shorter credentials yield what they have.
func Last4(credential string) string {
	r := []rune(credential)
	if len(r) <= 4 {
		return string(r)
	}
	return string(r[len(r)-4:])
}
