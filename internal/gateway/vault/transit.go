// Package vault provides a thin client for HashiCorp Vault's Transit and PKI
// secrets engines. It communicates via the Vault HTTP API (no heavy SDK
// dependency) and gracefully degrades when Vault is unavailable.
package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// TokenEncryptor encrypts and decrypts opaque tokens via Vault Transit.
// All methods are safe for concurrent use.
type TokenEncryptor interface {
	// Encrypt encrypts plaintext and returns the Vault ciphertext
	// (e.g. "vault:v1:…"). If Vault is unavailable and graceful degradation
	// is enabled, the plaintext is returned unchanged.
	Encrypt(ctx context.Context, plaintext string) (string, error)

	// Decrypt decrypts Vault ciphertext and returns the original plaintext.
	// If the value does not look like Vault ciphertext (no "vault:v1:" prefix),
	// it is returned as-is (supports pre-existing unencrypted data).
	Decrypt(ctx context.Context, ciphertext string) (string, error)
}

// TransitClient implements [TokenEncryptor] using Vault's Transit secrets engine
// HTTP API. It is safe for concurrent use.
type TransitClient struct {
	addr      string
	token     string
	keyName   string
	mountPath string
	client    *http.Client

	mu       sync.RWMutex
	disabled bool // set true after repeated failures (graceful degradation)
}

// TransitOption configures a [TransitClient].
type TransitOption func(*TransitClient)

// WithMountPath overrides the Transit mount path (default: "transit").
func WithMountPath(p string) TransitOption {
	return func(c *TransitClient) { c.mountPath = p }
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) TransitOption {
	return func(c *TransitClient) { c.client = hc }
}

// NewTransitClient creates a Transit secrets engine client.
//
// addr is the Vault server address (e.g. "https://vault.openclaw.lan").
// token is the Vault authentication token.
// keyName is the Transit key name (e.g. "glyphoxa-bot-tokens").
func NewTransitClient(addr, token, keyName string, opts ...TransitOption) *TransitClient {
	c := &TransitClient{
		addr:      addr,
		token:     token,
		keyName:   keyName,
		mountPath: "transit",
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// vaultCiphertextPrefix is the prefix Vault Transit uses for ciphertext.
const vaultCiphertextPrefix = "vault:v1:"

// Encrypt encrypts plaintext via the Transit encrypt API.
func (c *TransitClient) Encrypt(ctx context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if c.isDisabled() {
		return plaintext, nil
	}

	url := fmt.Sprintf("%s/v1/%s/encrypt/%s", c.addr, c.mountPath, c.keyName)

	body := transitEncryptRequest{Plaintext: plaintext}
	result, err := c.doTransitRequest(ctx, url, body)
	if err != nil {
		slog.Warn("vault: transit encrypt failed, returning plaintext (graceful degradation)", "err", err)
		return plaintext, nil
	}

	ct, ok := result.Data.Ciphertext()
	if !ok {
		return plaintext, nil
	}
	return ct, nil
}

// Decrypt decrypts Vault ciphertext via the Transit decrypt API.
func (c *TransitClient) Decrypt(ctx context.Context, ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	// Not vault-encrypted — return as-is (pre-existing plaintext data).
	if len(ciphertext) < len(vaultCiphertextPrefix) || ciphertext[:len(vaultCiphertextPrefix)] != vaultCiphertextPrefix {
		return ciphertext, nil
	}
	if c.isDisabled() {
		slog.Warn("vault: transit disabled but ciphertext requires decryption — returning error")
		return "", fmt.Errorf("vault: transit disabled, cannot decrypt ciphertext")
	}

	url := fmt.Sprintf("%s/v1/%s/decrypt/%s", c.addr, c.mountPath, c.keyName)

	body := transitDecryptRequest{Ciphertext: ciphertext}
	result, err := c.doTransitRequest(ctx, url, body)
	if err != nil {
		return "", fmt.Errorf("vault: transit decrypt: %w", err)
	}

	pt, ok := result.Data.Plaintext()
	if !ok {
		return "", fmt.Errorf("vault: transit decrypt: missing plaintext in response")
	}
	return pt, nil
}

// Ping checks connectivity to Vault by reading sys/health.
func (c *TransitClient) Ping(ctx context.Context) error {
	url := fmt.Sprintf("%s/v1/sys/health", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("vault: create health request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		c.markDisabled()
		return fmt.Errorf("vault: health check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.markDisabled()
		return fmt.Errorf("vault: health check returned %d", resp.StatusCode)
	}

	c.markEnabled()
	return nil
}

func (c *TransitClient) isDisabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.disabled
}

func (c *TransitClient) markDisabled() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.disabled {
		slog.Warn("vault: marking transit client as disabled (graceful degradation)")
		c.disabled = true
	}
}

func (c *TransitClient) markEnabled() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled {
		slog.Info("vault: transit client re-enabled after successful health check")
		c.disabled = false
	}
}

// ── request / response types ────────────────────────────────────────────────

type transitEncryptRequest struct {
	Plaintext string `json:"plaintext"`
}

type transitDecryptRequest struct {
	Ciphertext string `json:"ciphertext"`
}

type transitResponseData map[string]any

func (d transitResponseData) Ciphertext() (string, bool) {
	v, ok := d["ciphertext"].(string)
	return v, ok
}

func (d transitResponseData) Plaintext() (string, bool) {
	v, ok := d["plaintext"].(string)
	return v, ok
}

type transitResponse struct {
	Data transitResponseData `json:"data"`
}

func (c *TransitClient) doTransitRequest(ctx context.Context, url string, reqBody any) (*transitResponse, error) {
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		c.markDisabled()
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result transitResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	c.markEnabled()
	return &result, nil
}

// ── NoopEncryptor ───────────────────────────────────────────────────────────

// Compile-time interface assertion.
var _ TokenEncryptor = (*NoopEncryptor)(nil)

// NoopEncryptor is a no-op implementation of [TokenEncryptor] that passes
// values through unchanged. Used when Vault is not configured.
type NoopEncryptor struct{}

// Encrypt returns plaintext unchanged.
func (NoopEncryptor) Encrypt(_ context.Context, plaintext string) (string, error) {
	return plaintext, nil
}

// Decrypt returns ciphertext unchanged.
func (NoopEncryptor) Decrypt(_ context.Context, ciphertext string) (string, error) {
	return ciphertext, nil
}
