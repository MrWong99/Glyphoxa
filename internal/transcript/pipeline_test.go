package transcript

import (
	"testing"
)

func TestMaxWordCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		entities []string
		want     int
	}{
		{"empty", nil, 1},
		{"single word entities", []string{"Eldrinax", "Greymantle"}, 1},
		{"multi-word entity", []string{"Tower of Whispers", "Eldrinax"}, 3},
		{"mixed", []string{"A", "Tower of Whispers", "The Great Hall"}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := maxWordCount(tt.entities)
			if got != tt.want {
				t.Errorf("maxWordCount(%v) = %d, want %d", tt.entities, got, tt.want)
			}
		})
	}
}

func TestWithMinWordsForLLM_Option(t *testing.T) {
	t.Parallel()

	p := NewPipeline(WithMinWordsForLLM(3))
	if p.minWordsForLLM != 3 {
		t.Errorf("minWordsForLLM = %d, want 3", p.minWordsForLLM)
	}
}

func TestNewPipeline_Defaults(t *testing.T) {
	t.Parallel()

	p := NewPipeline()
	if p.llmThreshold != defaultLLMConfidenceThreshold {
		t.Errorf("llmThreshold = %f, want %f", p.llmThreshold, defaultLLMConfidenceThreshold)
	}
	if p.minWordsForLLM != defaultMinWordsForLLM {
		t.Errorf("minWordsForLLM = %d, want %d", p.minWordsForLLM, defaultMinWordsForLLM)
	}
	if p.phonetic != nil {
		t.Error("phonetic should be nil by default")
	}
	if p.llmCorrector != nil {
		t.Error("llmCorrector should be nil by default")
	}
}

func TestWithLLMOnLowConfidence_Option(t *testing.T) {
	t.Parallel()

	p := NewPipeline(WithLLMOnLowConfidence(0.8))
	if p.llmThreshold != 0.8 {
		t.Errorf("llmThreshold = %f, want 0.8", p.llmThreshold)
	}
}

func TestCollectLowConfidenceSpans_EmptyWords(t *testing.T) {
	t.Parallel()

	p := NewPipeline()
	spans := p.collectLowConfidenceSpans(nil, nil)
	if spans != nil {
		t.Errorf("expected nil spans for nil words, got %v", spans)
	}
}
