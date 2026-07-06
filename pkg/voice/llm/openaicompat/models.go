package openaicompat

import (
	"context"
	"fmt"
)

// ListModels returns the ids of the models the configured endpoint exposes via
// the OpenAI-compatible GET {baseURL}/models list call (the "data[].id" array).
// The list is UNFILTERED — every id the provider returns, in the provider's own
// order — so pinning a default first, sorting, or dropping non-chat models is the
// caller's concern (the Configuration model select does that in internal/rpc).
//
// A missing key surfaces the same request-time "missing API key" error as
// [Client.Complete] rather than reaching the endpoint; a non-2xx response or a
// transport failure is returned verbatim (wrapped with the provider name), so the
// caller can decide how to degrade — it is never a silent empty list.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("%s.ListModels: missing API key%s", c.name, c.missingKeyHint())
	}
	page, err := c.oai.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s.ListModels: %w", c.name, err)
	}
	ids := make([]string, 0, len(page.Data))
	for _, m := range page.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
