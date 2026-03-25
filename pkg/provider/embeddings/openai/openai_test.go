package openai

import (
	"testing"
	"time"
)

// TestModelDimensions_TextEmbedding3Small verifies 1536 dims for 3-small.
func TestModelDimensions_TextEmbedding3Small(t *testing.T) {
	d := modelDimensions("text-embedding-3-small")
	if d != 1536 {
		t.Errorf("text-embedding-3-small: expected 1536 dimensions, got %d", d)
	}
}

// TestModelDimensions_TextEmbedding3Large verifies 3072 dims for 3-large.
func TestModelDimensions_TextEmbedding3Large(t *testing.T) {
	d := modelDimensions("text-embedding-3-large")
	if d != 3072 {
		t.Errorf("text-embedding-3-large: expected 3072 dimensions, got %d", d)
	}
}

// TestModelDimensions_Ada002 verifies 1536 dims for ada-002.
func TestModelDimensions_Ada002(t *testing.T) {
	d := modelDimensions("text-embedding-ada-002")
	if d != 1536 {
		t.Errorf("text-embedding-ada-002: expected 1536 dimensions, got %d", d)
	}
}

// TestModelDimensions_Unknown verifies that unknown models return a positive default.
func TestModelDimensions_Unknown(t *testing.T) {
	d := modelDimensions("some-future-model")
	if d <= 0 {
		t.Errorf("unknown model: expected positive dimensions, got %d", d)
	}
}

// TestDimensions_MethodMatchesHelper verifies Provider.Dimensions() matches modelDimensions().
func TestDimensions_MethodMatchesHelper(t *testing.T) {
	cases := []string{
		"text-embedding-3-small",
		"text-embedding-3-large",
		"text-embedding-ada-002",
	}
	for _, model := range cases {
		p := &Provider{model: model}
		if got := p.Dimensions(); got != modelDimensions(model) {
			t.Errorf("model %s: Dimensions() = %d, want %d", model, got, modelDimensions(model))
		}
	}
}

// TestModelID verifies that ModelID returns the model string as-is.
func TestModelID(t *testing.T) {
	cases := []string{
		"text-embedding-3-small",
		"text-embedding-3-large",
		"text-embedding-ada-002",
		"my-custom-embeddings-model",
	}
	for _, model := range cases {
		p := &Provider{model: model}
		if got := p.ModelID(); got != model {
			t.Errorf("ModelID() = %q, want %q", got, model)
		}
	}
}

// TestNew_DefaultModel verifies that an empty model string defaults to text-embedding-3-small.
func TestNew_DefaultModel(t *testing.T) {
	p, err := New("sk-test", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ModelID() != DefaultModel {
		t.Errorf("expected default model %s, got %s", DefaultModel, p.ModelID())
	}
}

// TestNew_MissingAPIKey checks that an empty API key is rejected.
func TestNew_MissingAPIKey(t *testing.T) {
	_, err := New("", "text-embedding-3-small")
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}

// TestNew_Options verifies that options are accepted without error.
func TestNew_Options(t *testing.T) {
	_, err := New("sk-test", "text-embedding-3-small",
		WithBaseURL("https://custom.example.com"),
		WithOrganization("org-123"),
	)
	if err != nil {
		t.Fatalf("unexpected error with valid options: %v", err)
	}
}

// TestNew_WithTimeout verifies that the timeout option is accepted.
func TestNew_WithTimeout(t *testing.T) {
	t.Parallel()

	p, err := New("sk-test", "text-embedding-3-small",
		WithTimeout(30*time.Second),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestNew_WithAllOptions verifies that all options combined work correctly.
func TestNew_WithAllOptions(t *testing.T) {
	t.Parallel()

	p, err := New("sk-test", "text-embedding-3-large",
		WithBaseURL("https://custom.example.com"),
		WithOrganization("org-123"),
		WithTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.model != "text-embedding-3-large" {
		t.Errorf("model = %q, want %q", p.model, "text-embedding-3-large")
	}
}

// TestNew_CustomModel verifies that a custom model is preserved.
func TestNew_CustomModel(t *testing.T) {
	t.Parallel()

	p, err := New("sk-test", "my-custom-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.model != "my-custom-model" {
		t.Errorf("model = %q, want %q", p.model, "my-custom-model")
	}
}

// TestModelDimensions_CaseInsensitive verifies case insensitivity.
func TestModelDimensions_CaseInsensitive(t *testing.T) {
	t.Parallel()

	lower := modelDimensions("text-embedding-3-large")
	upper := modelDimensions("TEXT-EMBEDDING-3-LARGE")
	if lower != upper {
		t.Errorf("case should not matter: got %d vs %d", lower, upper)
	}
}

// TestFloat64ToFloat32_Empty verifies empty slice handling.
func TestFloat64ToFloat32_Empty(t *testing.T) {
	t.Parallel()

	out := float64ToFloat32(nil)
	if len(out) != 0 {
		t.Errorf("expected empty result for nil input, got len %d", len(out))
	}
}

// TestFloat64ToFloat32_Single verifies single-element conversion.
func TestFloat64ToFloat32_Single(t *testing.T) {
	t.Parallel()

	out := float64ToFloat32([]float64{3.14})
	if len(out) != 1 {
		t.Fatalf("expected 1 element, got %d", len(out))
	}
	if out[0] != float32(3.14) {
		t.Errorf("expected %v, got %v", float32(3.14), out[0])
	}
}

// TestDimensions_CustomModel verifies that custom models return default dimensions.
func TestDimensions_CustomModel(t *testing.T) {
	t.Parallel()

	p := &Provider{model: "my-custom-embeddings"}
	got := p.Dimensions()
	if got != 1536 {
		t.Errorf("expected default 1536 dimensions for unknown model, got %d", got)
	}
}

// TestFloat64ToFloat32 verifies the conversion helper.
func TestFloat64ToFloat32(t *testing.T) {
	in := []float64{1.0, 2.5, -0.5}
	out := float64ToFloat32(in)
	if len(out) != len(in) {
		t.Fatalf("expected %d elements, got %d", len(in), len(out))
	}
	for i, v := range out {
		expected := float32(in[i])
		if v != expected {
			t.Errorf("index %d: expected %v, got %v", i, expected, v)
		}
	}
}
