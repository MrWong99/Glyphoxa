package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// CertBundle holds a certificate, private key, and CA chain issued by Vault PKI.
type CertBundle struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
	CAChain     string `json:"ca_chain"`
	Expiration  int64  `json:"expiration"`
}

// ExpiresAt returns the expiration time of the certificate.
func (b CertBundle) ExpiresAt() time.Time {
	return time.Unix(b.Expiration, 0)
}

// PKIClient issues short-lived TLS certificates from Vault's PKI secrets engine.
// It is safe for concurrent use.
type PKIClient struct {
	addr      string
	token     string
	mountPath string
	roleName  string
	client    *http.Client
}

// PKIOption configures a [PKIClient].
type PKIOption func(*PKIClient)

// WithPKIMountPath overrides the PKI mount path (default: "pki").
func WithPKIMountPath(p string) PKIOption {
	return func(c *PKIClient) { c.mountPath = p }
}

// WithPKIHTTPClient overrides the default HTTP client.
func WithPKIHTTPClient(hc *http.Client) PKIOption {
	return func(c *PKIClient) { c.client = hc }
}

// NewPKIClient creates a PKI secrets engine client.
//
// addr is the Vault server address (e.g. "https://vault.openclaw.lan").
// token is the Vault authentication token.
// roleName is the PKI role (e.g. "glyphoxa-grpc").
func NewPKIClient(addr, token, roleName string, opts ...PKIOption) *PKIClient {
	c := &PKIClient{
		addr:      addr,
		token:     token,
		mountPath: "pki",
		roleName:  roleName,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// IssueCert requests a certificate from Vault PKI for the given common name
// and optional SANs. The TTL controls certificate lifetime.
func (c *PKIClient) IssueCert(ctx context.Context, commonName string, sans []string, ttl time.Duration) (*CertBundle, error) {
	url := fmt.Sprintf("%s/v1/%s/issue/%s", c.addr, c.mountPath, c.roleName)

	reqBody := map[string]any{
		"common_name": commonName,
		"ttl":         ttl.String(),
	}
	if len(sans) > 0 {
		reqBody["alt_names"] = joinComma(sans)
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vault: marshal PKI request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("vault: create PKI request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault: PKI issue request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vault: read PKI response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault: PKI returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data struct {
			Certificate string   `json:"certificate"`
			PrivateKey  string   `json:"private_key"`
			CAChain     []string `json:"ca_chain"`
			Expiration  int64    `json:"expiration"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("vault: unmarshal PKI response: %w", err)
	}

	caChain := ""
	for i, cert := range result.Data.CAChain {
		if i > 0 {
			caChain += "\n"
		}
		caChain += cert
	}

	return &CertBundle{
		Certificate: result.Data.Certificate,
		PrivateKey:  result.Data.PrivateKey,
		CAChain:     caChain,
		Expiration:  result.Data.Expiration,
	}, nil
}

// WriteToDisk writes the cert bundle to the given directory and returns the file paths.
// Files are written as cert.pem, key.pem, and ca.pem.
func (b CertBundle) WriteToDisk(dir string) (certPath, keyPath, caPath string, err error) {
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	caPath = filepath.Join(dir, "ca.pem")

	if err = os.WriteFile(certPath, []byte(b.Certificate), 0600); err != nil {
		return "", "", "", fmt.Errorf("vault: write cert: %w", err)
	}
	if err = os.WriteFile(keyPath, []byte(b.PrivateKey), 0600); err != nil {
		return "", "", "", fmt.Errorf("vault: write key: %w", err)
	}
	if err = os.WriteFile(caPath, []byte(b.CAChain), 0600); err != nil {
		return "", "", "", fmt.Errorf("vault: write CA: %w", err)
	}

	slog.Info("vault: PKI certs written to disk",
		"cert", certPath, "key", keyPath, "ca", caPath,
		"expires", b.ExpiresAt().Format(time.RFC3339),
	)
	return certPath, keyPath, caPath, nil
}

func joinComma(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += "," + s
	}
	return result
}
