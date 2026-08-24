//go:build jsonv2bench

// Package-local comparison variant: run with
//
//	go test ./pkg/voice/stt/elevenlabs -tags=jsonv2bench -run XXX -bench Decode
//
// to put the current v1 encoding/json decode side by side with Go 1.27's
// explicit encoding/json/v2 API over the same fixture frames. It is build-tag
// guarded because v2 is stricter than v1 (duplicate keys rejected, invalid
// UTF-8 rejected) — the tag keeps that strictness out of the default build
// until an adoption decision is actually filed (#609).

package elevenlabs

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

// decodeSTTFrameV2 is [decodeSTTFrame] with the explicit v2 unmarshaler; the
// struct shape is identical so the two benchmarks differ only in the decoder.
func decodeSTTFrameV2(data []byte) (messageType, text, errText string, err error) {
	var msg struct {
		MessageType string `json:"message_type"`
		Text        string `json:"text"`
		Error       string `json:"error"`
	}
	if err := jsonv2.Unmarshal(data, &msg); err != nil {
		return "", "", "", err
	}
	return msg.MessageType, msg.Text, msg.Error, nil
}

// BenchmarkReadPumpDecodeV2 mirrors [BenchmarkReadPumpDecode] on encoding/json/v2.
func BenchmarkReadPumpDecodeV2(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, _, _, err := decodeSTTFrameV2(sttFrames[i%len(sttFrames)].raw); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkReadPumpDecodePartialV2 mirrors [BenchmarkReadPumpDecodePartial].
func BenchmarkReadPumpDecodePartialV2(b *testing.B) {
	raw := sttFrames[2].raw // partial_transcript_long
	b.ReportAllocs()
	for b.Loop() {
		if _, _, _, err := decodeSTTFrameV2(raw); err != nil {
			b.Fatal(err)
		}
	}
}
