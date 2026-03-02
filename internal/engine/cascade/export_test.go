package cascade

import "github.com/MrWong99/glyphoxa/pkg/provider/llm"

// FirstSentenceBoundaryForTest is a test-only export of firstSentenceBoundary.
func FirstSentenceBoundaryForTest(s string) int {
	return firstSentenceBoundary(s)
}

// AccumulateToolCallsForTest is a test-only export of accumulateToolCalls.
func AccumulateToolCallsForTest(existing, incoming []llm.ToolCall) []llm.ToolCall {
	return accumulateToolCalls(existing, incoming)
}
