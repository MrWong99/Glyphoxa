package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// modelsPageSize is the page size asked of GET /v1/models. The catalog is a
	// few dozen ids; one page normally carries all of it.
	modelsPageSize = "100"
	// modelsMaxPages bounds the pagination walk, so a server that always
	// answers has_more cannot hold a caller in a loop.
	modelsMaxPages = 10
	// modelsMaxBody caps one catalog page read.
	modelsMaxBody = 1 << 20
)

// modelsPage is the wire shape of one GET /v1/models page.
type modelsPage struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModels returns the ids of the models the key can use, from the Models API
// (GET {baseURL}/v1/models), following has_more / last_id to the end. The list
// is UNFILTERED and in the API's own order (newest first), the same contract as
// the OpenAI-compatible adapters' ListModels: pinning a default, sorting or
// curating is the caller's concern.
//
// A missing key is the same request-time "missing API key" error as
// [Client.Complete]; a non-2xx answer is a [providererr.HTTPError], so a caller
// can tell a rejected key from an outage. It is never a silent empty list.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("anthropic.ListModels: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	var (
		ids   []string
		after string
	)
	for range modelsMaxPages {
		page, err := c.modelsPage(ctx, after)
		if err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			ids = append(ids, m.ID)
		}
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return ids, nil
}

// modelsPage fetches one catalog page, the one after id `after` ("" = first).
func (c *Client) modelsPage(ctx context.Context, after string) (modelsPage, error) {
	q := url.Values{"limit": {modelsPageSize}}
	if after != "" {
		q.Set("after_id", after)
	}
	u := strings.TrimRight(c.baseURL, "/") + "/v1/models?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return modelsPage{}, fmt.Errorf("anthropic.ListModels: build request: %w", err)
	}
	setAuth(req.Header, c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return modelsPage{}, fmt.Errorf("anthropic.ListModels: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modelsPage{}, readErrorResponse(resp, "ListModels")
	}
	var page modelsPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, modelsMaxBody)).Decode(&page); err != nil {
		return modelsPage{}, fmt.Errorf("anthropic.ListModels: decode: %w", err)
	}
	return page, nil
}
