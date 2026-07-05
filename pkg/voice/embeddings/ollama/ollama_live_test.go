package ollama_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/ollama"
)

// ollamaBaseURL resolves the live-test endpoint from GLYPHOXA_OLLAMA_URL (the
// env var the consumer wiring reads in #116), defaulting to the loopback
// server. Test-only wiring — the adapter itself takes an explicit base URL.
func ollamaBaseURL() string {
	if u := os.Getenv("GLYPHOXA_OLLAMA_URL"); u != "" {
		return u
	}
	return ollama.DefaultBaseURL
}

// probeOllama checks that a live Ollama is reachable AND has the embedding model
// pulled, by reading GET /api/tags within a short timeout. It returns "" when
// ready, or a human reason to skip: a black-holed or absent endpoint, or a
// server that is up but missing the model (the operator must `ollama pull`).
func probeOllama(base string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/tags", nil)
	if err != nil {
		return "could not build /api/tags request: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "no reachable Ollama at " + base + ": " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "Ollama /api/tags returned HTTP " + resp.Status
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return "could not decode /api/tags: " + err.Error()
	}
	for _, m := range tags.Models {
		if strings.HasPrefix(m.Name, ollama.DefaultModel) {
			return ""
		}
	}
	return "Ollama is up but model " + ollama.DefaultModel + " is not pulled (run `ollama pull " + ollama.DefaultModel + "`)"
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TestLive_Ollama_RelatedSentencesAreCloser is the AC3 live property: against a
// real nomic-embed-text, two semantically related Campaign sentences sit closer
// in cosine similarity than either does to an unrelated sentence. It asserts a
// tolerant ordering property — never exact vector values, which drift with model
// versions. Skipped LOUDLY (not failed) when no live Ollama with the model is
// reachable, so a default `go test ./...` on a model-less box stays green
// without pretending it exercised the real embedder.
func TestLive_Ollama_RelatedSentencesAreCloser(t *testing.T) {
	base := ollamaBaseURL()
	if reason := probeOllama(base); reason != "" {
		t.Skipf("SKIPPED LIVE OLLAMA EMBED TEST — %s. This test embeds real "+
			"Campaign sentences against %s and was NOT run. Set GLYPHOXA_OLLAMA_URL "+
			"to point at a reachable server with the model pulled to run it.",
			reason, ollama.DefaultModel)
	}

	// Two related sentences about a dragon hoard, one unrelated about a tavern.
	const (
		a = "The ancient dragon guards a vast hoard of gold deep in the mountain."
		b = "A great wyrm sleeps atop its treasure beneath the northern peaks."
		c = "The cheerful tavern keeper poured another round of ale for the bards."
	)

	c1 := ollama.New(ollama.WithBaseURL(base))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := c1.Embed(ctx, []string{a, b, c})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	for i, v := range out {
		if len(v) != embeddings.Dim {
			t.Fatalf("vector %d len = %d, want %d", i, len(v), embeddings.Dim)
		}
	}

	simAB := cosine(out[0], out[1]) // related
	simAC := cosine(out[0], out[2]) // unrelated
	simBC := cosine(out[1], out[2]) // unrelated
	t.Logf("cos(related a,b)=%.4f  cos(a,c)=%.4f  cos(b,c)=%.4f", simAB, simAC, simBC)

	if !(simAB > simAC && simAB > simBC) {
		t.Errorf("related pair not closest: cos(a,b)=%.4f must exceed cos(a,c)=%.4f and cos(b,c)=%.4f",
			simAB, simAC, simBC)
	}
}
