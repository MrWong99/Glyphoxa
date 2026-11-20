package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// embedRequest is the POST /api/embed body. Ollama's batch embed endpoint takes
// a string array in `input` and returns one vector per element.
//
// keep_alive pins the model in memory. Ollama unloads an idle model after five
// minutes by default, and a reload takes seconds — longer than the memory-recall
// budget of a live turn — so a table that paused for a few minutes would lose NPC
// memory on its next turns. Worse, a caller that gives up mid-load makes Ollama
// abort the load, so short-deadline callers alone never get the model back.
type embedRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	KeepAlive int      `json:"keep_alive"`
}

// keepAliveForever is Ollama's "never unload" keep_alive value (any negative number).
const keepAliveForever = -1

// embedResponse is the POST /api/embed response. `embeddings` carries one
// vector per input, in input order.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed implements [embeddings.Provider]. One call → one POST /api/embed
// request → len(texts) vectors in input order, each [embeddings.Dim] long.
//
// The response is validated hard: the vector count must equal len(texts) and
// every vector's length must equal [embeddings.Dim]. A wrong dimension (e.g. a
// mis-pulled model) returns an error naming the model, the wanted dimension,
// and the got dimension — the adapter never truncates or pads.
//
// An empty input returns (nil, nil) without a network call: there is nothing to
// embed and the endpoint would reject an empty batch.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Model: c.model, Input: texts, KeepAlive: keepAliveForever})
	if err != nil {
		return nil, fmt.Errorf("ollama.Embed: marshal request: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama.Embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama.Embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorResponse(resp)
	}

	var decoded embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("ollama.Embed: decode body: %w", err)
	}

	if n := len(decoded.Embeddings); n != len(texts) {
		return nil, fmt.Errorf("ollama.Embed: model %q returned %d vectors for %d inputs",
			c.model, n, len(texts))
	}
	for i, v := range decoded.Embeddings {
		if len(v) != embeddings.Dim {
			return nil, fmt.Errorf("ollama.Embed: model %q returned a %d-dim vector at index %d, want %d",
				c.model, len(v), i, embeddings.Dim)
		}
	}
	return decoded.Embeddings, nil
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and wraps it as an error naming the provider and status.
func readErrorResponse(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("ollama.Embed: HTTP %d %s: %s",
		resp.StatusCode, resp.Status, strings.TrimSpace(string(snippet)))
}
