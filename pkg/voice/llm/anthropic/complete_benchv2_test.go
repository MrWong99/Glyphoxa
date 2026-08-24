//go:build jsonv2bench

// Package-local comparison variant: run with
//
//	go test ./pkg/voice/llm/anthropic -tags=jsonv2bench -run XXX -bench Decode
//
// to put the current v1 encoding/json decode side by side with Go 1.27's
// explicit encoding/json/v2 API over the same fixture events. Tag-guarded
// because v2 rejects duplicate keys, and Anthropic frames carry LLM-authored
// JSON (tool inputs) whose duplicate-key tolerance would have to be audited
// before any adoption (#609).

package anthropic

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

// BenchmarkStreamEventsDecodeV2 mirrors [BenchmarkStreamEventsDecode] on
// encoding/json/v2, decoding into the same production [sseEvent].
func BenchmarkStreamEventsDecodeV2(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		var ev sseEvent
		if err := jsonv2.Unmarshal(sseFrames[i%len(sseFrames)].raw, &ev); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkStreamEventsDecodeTextDeltaV2 mirrors [BenchmarkStreamEventsDecodeTextDelta].
func BenchmarkStreamEventsDecodeTextDeltaV2(b *testing.B) {
	raw := sseFrames[3].raw // content_block_delta_text
	b.ReportAllocs()
	for b.Loop() {
		var ev sseEvent
		if err := jsonv2.Unmarshal(raw, &ev); err != nil {
			b.Fatal(err)
		}
	}
}
