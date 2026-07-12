package agenttool

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// TestForcedToolChoice_OneShotConsumedOnFirstCall pins the #399 seam #398 ships: a
// ctx carrying [withForcedToolChoice] hands the override to the FIRST completion's
// [providerAdapter.requestedChoice] and then reverts to auto, so only the turn's
// opening round is forced. A ctx without an override always yields auto.
func TestForcedToolChoice_OneShotConsumedOnFirstCall(t *testing.T) {
	a := providerAdapter{}

	// No override → auto, every call.
	if got := a.requestedChoice(context.Background()); got.Mode != llm.ToolChoiceAuto {
		t.Errorf("requestedChoice with no override = %+v, want auto", got)
	}

	ctx := withForcedToolChoice(context.Background(), llm.ToolChoice{Mode: llm.ToolChoiceRequired})
	if got := a.requestedChoice(ctx); got.Mode != llm.ToolChoiceRequired {
		t.Errorf("first requestedChoice = %+v, want the forced required override", got)
	}
	if got := a.requestedChoice(ctx); got.Mode != llm.ToolChoiceAuto {
		t.Errorf("second requestedChoice = %+v, want auto (override is one-shot)", got)
	}
}
