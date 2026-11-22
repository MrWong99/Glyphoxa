//go:build live

// Live check of the voice model's request shape (#702). Excluded from the
// default keyless suite by the `live` tag: it makes PAID calls to the real
// Anthropic endpoint and only runs with `go test -tags=live` and
// ANTHROPIC_API_KEY set (an API key, or the `ant` CLI's OAuth access token).
package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// usageSpy forwards requests to the real API and keeps the usage object of
// every response's message_start, which the adapter folds into one input count.
type usageSpy struct {
	mu     sync.Mutex
	usages []map[string]any
}

func (s *usageSpy) server(t *testing.T) *httptest.Server {
	t.Helper()
	target, _ := url.Parse(anthropic.DefaultBaseURL)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = target.Host
		r.Out.Header.Del("Accept-Encoding") // keep the SSE body readable
	}}
	proxy.ModifyResponse = func(resp *http.Response) error {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		for _, line := range strings.Split(string(body), "\n") {
			data, ok := strings.CutPrefix(line, "data:")
			if !ok || !strings.Contains(data, `"message_start"`) {
				continue
			}
			var ev struct {
				Message struct {
					Usage map[string]any `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				s.mu.Lock()
				s.usages = append(s.usages, ev.Message.Usage)
				s.mu.Unlock()
			}
		}
		return nil
	}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	return srv
}

// TestLive_Haiku55_SecondRequestReadsThePromptCache proves the caching shape
// against the real API: the same stable system prompt sent twice is written to
// the cache by the first request and read from it by the second, and the
// adapter's reported input usage is the full prompt both times.
func TestLive_Haiku55_SecondRequestReadsThePromptCache(t *testing.T) {
	if os.Getenv(anthropic.APIKeyEnv) == "" {
		t.Skipf("%s not set", anthropic.APIKeyEnv)
	}
	spy := &usageSpy{}
	c := anthropic.New("", anthropic.WithBaseURL(spy.server(t).URL))

	// Comfortably past the model's 512-token minimum cacheable prefix, and
	// unique per run so the first request is a write, not a read of an earlier
	// run's cache entry.
	stable := "Du bist Flo, die Wirtin der Taverne Zur Goldenen Gans. Lauf " + t.Name() + " " + strings.Repeat("Du redest kurz, herzlich und direkt, wie man am Tresen redet. ", 120) + os.Getenv("GX_CACHE_NONCE")
	ask := func(question string) int {
		ch, err := c.Complete(context.Background(), llm.Request{
			Model: anthropic.HaikuVoiceModel,
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Text: stable},
				{Role: llm.RoleUser, Text: question},
				{Role: llm.RoleSystem, Text: "## Facts\nHeute gibt es Eintopf."},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		input := -1
		for ev := range ch {
			switch ev.Type {
			case llm.EventUsage:
				input = ev.Usage.InputTokens
			case llm.EventError:
				t.Fatalf("stream error: %s", ev.Err)
			}
		}
		return input
	}

	first, second := ask("Hallo Flo!"), ask("Was gibt es heute?")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.usages) != 2 {
		t.Fatalf("saw %d responses, want 2", len(spy.usages))
	}
	num := func(u map[string]any, k string) int { f, _ := u[k].(float64); return int(f) }
	for i, u := range spy.usages {
		t.Logf("request %d: input_tokens=%d cache_creation_input_tokens=%d cache_read_input_tokens=%d",
			i+1, num(u, "input_tokens"), num(u, "cache_creation_input_tokens"), num(u, "cache_read_input_tokens"))
	}
	if num(spy.usages[0], "cache_creation_input_tokens") == 0 {
		t.Errorf("first request wrote nothing to the cache: %v", spy.usages[0])
	}
	if num(spy.usages[1], "cache_read_input_tokens") == 0 {
		t.Errorf("second request read nothing from the cache: %v", spy.usages[1])
	}
	for i, got := range []int{first, second} {
		u := spy.usages[i]
		if want := num(u, "input_tokens") + num(u, "cache_creation_input_tokens") + num(u, "cache_read_input_tokens"); got != want {
			t.Errorf("request %d: adapter reported %d input tokens, want the whole prompt %d", i+1, got, want)
		}
	}
}
