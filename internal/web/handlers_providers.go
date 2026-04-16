package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// ProviderConfig represents a configured provider and its test status.
type ProviderConfig struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Latency  int    `json:"latency_ms,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleTestProvider tests a provider connection by making a minimal API call.
// This is a lightweight check — it validates API key / connectivity, not full functionality.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	var body struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		BaseURL  string `json:"base_url,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Type == "" || body.Provider == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "type and provider are required")
		return
	}

	// Validate base URL to prevent SSRF attacks — block private IPs,
	// internal DNS, cloud metadata endpoints, and K8s service addresses.
	if err := validateBaseURL(body.BaseURL, nil); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_base_url", err.Error())
		return
	}

	start := time.Now()
	testErr := testProviderConnection(r, body.Type, body.Provider, body.APIKey, body.BaseURL)
	latency := time.Since(start).Milliseconds()

	result := ProviderConfig{
		Type:     body.Type,
		Provider: body.Provider,
		Status:   "ok",
		Latency:  int(latency),
	}
	if testErr != nil {
		result.Status = "error"
		result.Error = testErr.Error()
	}

	s.auditLog(r, "provider.test", "provider", body.Type+"/"+body.Provider, nil)

	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// testProviderConnection performs a lightweight connectivity test for a provider.
func testProviderConnection(r *http.Request, providerType, provider, apiKey, baseURL string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	switch providerType {
	case "llm":
		return testLLMProvider(r, client, provider, apiKey, baseURL)
	case "stt":
		return testSTTProvider(r, client, provider, apiKey, baseURL)
	case "tts":
		return testTTSProvider(r, client, provider, apiKey, baseURL)
	default:
		return fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

func testLLMProvider(r *http.Request, client *http.Client, provider, apiKey, baseURL string) error {
	var endpoint string
	var authHeader string

	switch provider {
	case "openai":
		endpoint = buildProviderURL(baseURL, "https://api.openai.com", "/v1/models")
		authHeader = "Bearer " + apiKey
	case "anthropic":
		endpoint = buildProviderURL(baseURL, "https://api.anthropic.com", "/v1/models")
		authHeader = apiKey // Anthropic uses x-api-key header
	case "google", "gemini":
		endpoint = fmt.Sprintf("https://generativelanguage.googleapis.com/v1/models?key=%s", url.QueryEscape(apiKey))
	default:
		return fmt.Errorf("unsupported LLM provider: %s", provider)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if provider == "anthropic" {
		req.Header.Set("x-api-key", authHeader)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}

	slog.Info("web: provider test ok", "type", "llm", "provider", provider, "status", resp.StatusCode)
	return nil
}

func testSTTProvider(r *http.Request, client *http.Client, provider, apiKey, baseURL string) error {
	var endpoint string
	switch provider {
	case "deepgram":
		endpoint = buildProviderURL(baseURL, "https://api.deepgram.com", "/v1/projects")
	case "whisper", "openai":
		endpoint = buildProviderURL(baseURL, "https://api.openai.com", "/v1/models")
	default:
		return fmt.Errorf("unsupported STT provider: %s", provider)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if provider == "deepgram" {
		req.Header.Set("Authorization", "Token "+apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}

	return nil
}

func testTTSProvider(r *http.Request, client *http.Client, provider, apiKey, baseURL string) error {
	var endpoint string
	switch provider {
	case "elevenlabs":
		endpoint = buildProviderURL(baseURL, "https://api.elevenlabs.io", "/v1/voices")
	case "openai":
		endpoint = buildProviderURL(baseURL, "https://api.openai.com", "/v1/models")
	default:
		return fmt.Errorf("unsupported TTS provider: %s", provider)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if provider == "elevenlabs" {
		req.Header.Set("xi-api-key", apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}

	return nil
}
