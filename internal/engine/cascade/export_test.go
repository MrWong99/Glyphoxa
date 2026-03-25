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

// TruncateForTest is a test-only export of truncate.
func TruncateForTest(s string, maxLen int) string {
	return truncate(s, maxLen)
}

// IsDecimalDotForTest is a test-only export of isDecimalDot.
func IsDecimalDotForTest(s string, i int) bool {
	return isDecimalDot(s, i)
}

// IsEllipsisDotForTest is a test-only export of isEllipsisDot.
func IsEllipsisDotForTest(s string, i int) bool {
	return isEllipsisDot(s, i)
}

// IsSentenceWhitespaceForTest is a test-only export of isSentenceWhitespace.
func IsSentenceWhitespaceForTest(b byte) bool {
	return isSentenceWhitespace(b)
}

// LastUserMessageForTest is a test-only export of lastUserMessage.
func LastUserMessageForTest(msgs []llm.Message) (llm.Message, bool) {
	return lastUserMessage(msgs)
}
