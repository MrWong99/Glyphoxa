package ollama_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/ollama"
)

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
