package ollama_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/ollama"
)

// fullVectorJSON builds the `/api/embed` response body carrying one
// [embeddings.Dim] vector per marker; every element of vector i is markers[i],
// so callers can pin per-input identity (and thus order) without depending on
// exact float values.
func fullVectorJSON(t *testing.T, markers ...float32) string {
	t.Helper()
	vecs := make([][]float32, len(markers))
	for i, m := range markers {
		v := make([]float32, embeddings.Dim)
		for j := range v {
			v[j] = m
		}
		vecs[i] = v
	}
	buf, err := json.Marshal(struct {
		Embeddings [][]float32 `json:"embeddings"`
	}{vecs})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(buf)
}

// Compile-time assertion: [ollama.Client] satisfies [embeddings.Provider],
// the only contract the async embedding worker (ADR-0011) depends on.
var _ embeddings.Provider = (*ollama.Client)(nil)

// dimVectorJSON builds the `/api/embed` response body carrying one vector of
// the given dimension, each element a distinct small float so a wrong length is
// the only thing under test.
func dimVectorJSON(dim int) string {
	var b strings.Builder
	b.WriteString(`{"embeddings":[[`)
	for i := 0; i < dim; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("0.1")
	}
	b.WriteString(`]]}`)
	return b.String()
}

// TestEmbed_WrongDimension_ErrorsNamingWantGotModel pins the AC1 guard: when
// the model returns any dimension other than [embeddings.Dim] the adapter MUST
// error (never truncate or pad), and the error names the model, the wanted
// dimension, and the got dimension so a mis-pulled model is diagnosable.
func TestEmbed_WrongDimension_ErrorsNamingWantGotModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(dimVectorJSON(embeddings.Dim - 1)))
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	_, err := c.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("Embed with wrong-dimension vector returned nil error")
	}
	got := err.Error()
	for _, must := range []string{ollama.DefaultModel, "767", "768"} {
		if !strings.Contains(got, must) {
			t.Errorf("error %q missing required substring %q", got, must)
		}
	}
}

// TestEmbed_Batch_ReturnsNVectorsInInputOrder pins AC2: N texts yield N vectors
// in input order. The server also captures the request so the wire shape is
// pinned in the same test — POST /api/embed with {"model":M,"input":[texts]} —
// and echoes a per-input marker vector so a scrambled order would fail the
// order assertion, not just the count.
func TestEmbed_Batch_ReturnsNVectorsInInputOrder(t *testing.T) {
	var sawMethod, sawPath, sawModel atomic.Value
	var sawInput atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawMethod.Store(r.Method)
		sawPath.Store(r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		sawModel.Store(req.Model)
		sawInput.Store(strings.Join(req.Input, "|"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Marker vectors: 1.0, 2.0, 3.0 in input order.
		_, _ = w.Write([]byte(fullVectorJSON(t, 1.0, 2.0, 3.0)))
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	texts := []string{"alpha", "beta", "gamma"}
	out, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(out) != len(texts) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(texts))
	}
	for i, want := range []float32{1.0, 2.0, 3.0} {
		if len(out[i]) != embeddings.Dim {
			t.Fatalf("out[%d] len = %d, want %d", i, len(out[i]), embeddings.Dim)
		}
		if out[i][0] != want {
			t.Errorf("out[%d][0] = %v, want %v (order scrambled?)", i, out[i][0], want)
		}
	}

	if got, _ := sawMethod.Load().(string); got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}
	if got, _ := sawPath.Load().(string); got != "/api/embed" {
		t.Errorf("path = %q, want /api/embed", got)
	}
	if got, _ := sawModel.Load().(string); got != ollama.DefaultModel {
		t.Errorf("model = %q, want %q", got, ollama.DefaultModel)
	}
	if got, _ := sawInput.Load().(string); got != "alpha|beta|gamma" {
		t.Errorf("input = %q, want %q", got, "alpha|beta|gamma")
	}
}

// TestEmbed_CountMismatch_Errors pins the second half of AC2's totality: a
// response with a different number of vectors than inputs is a protocol
// violation and must error rather than return a short/over-long slice.
func TestEmbed_CountMismatch_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Two vectors for three inputs.
		_, _ = w.Write([]byte(fullVectorJSON(t, 1.0, 2.0)))
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	_, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("Embed with 2 vectors for 3 inputs returned nil error")
	}
	for _, must := range []string{"2", "3", ollama.DefaultModel} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err.Error(), must)
		}
	}
}

// TestEmbed_Non2xx_WrapsProviderAndStatus pins the error surface: a non-2xx
// response yields an error naming the provider op and the HTTP status, with a
// body snippet for diagnostics — the shape on-call reviewers grep for.
func TestEmbed_Non2xx_WrapsProviderAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'nomic-embed-text' not found"}`))
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed with 404 returned nil error")
	}
	for _, must := range []string{"ollama.Embed", "404", "not found"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err.Error(), must)
		}
	}
}

// TestEmbed_MalformedJSON_WrapsDecodeError pins that a 200 with a non-JSON body
// fails as a wrapped decode error naming the provider, not a panic or silent
// empty result.
func TestEmbed_MalformedJSON_WrapsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"embeddings": not-json`))
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed with malformed JSON returned nil error")
	}
	if !strings.Contains(err.Error(), "ollama.Embed") {
		t.Errorf("error %q does not name the provider op", err.Error())
	}
}

// TestEmbed_EmptyInput_NoCallNoError pins the fast path: an empty batch returns
// (nil, nil) without any network call, so the worker can flush a zero-length
// batch harmlessly and a black-holed endpoint is never dialed for nothing.
func TestEmbed_EmptyInput_NoCallNoError(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := ollama.New(ollama.WithBaseURL(srv.URL))
	out, err := c.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if out != nil {
		t.Errorf("out = %v, want nil", out)
	}
	if called.Load() {
		t.Error("Embed(nil) made a network call; want none")
	}
}
