package storage

import (
	"encoding/json"
	"fmt"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// VoiceFromJSON decodes an Agent's persisted voice JSONB into a [tts.Voice]. It
// is the SINGLE canonical reader for the voice column, mirroring [GrantsFromRows]
// (#215): the voice pipeline's hydration and the Campaign RPC's editor mapping
// both go through it, so a writer and a reader can never drift into the silent
// NPC of issue #224.
//
// The CANONICAL SHAPE is the Go-default field names of tts.Voice
// ({"ProviderID","VoiceID","Name","Language","Settings"}) — the shape the seed
// rows already hold and the pipeline already reads. tts.Voice deliberately
// carries NO json tags (ADR-0022 keeps Settings opaque and the type untouched);
// tagging it would orphan every healthy row.
//
// An empty column or a bare {} — the schema default and the editor's "no voice"
// — decodes to the zero Voice with a nil error. A Settings persisted as the JSON
// literal null normalizes to a nil Settings so a settings-less Voice round-trips
// identically. A genuinely unparsable blob is an error, never a silent zero.
func VoiceFromJSON(raw json.RawMessage) (tts.Voice, error) {
	var v tts.Voice
	if len(raw) == 0 {
		return tts.Voice{}, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return tts.Voice{}, fmt.Errorf("storage: decode voice: %w", err)
	}
	// A nil Settings serializes to the JSON literal `null`, which unmarshals back
	// into a non-nil json.RawMessage("null"). Normalize it to nil so a
	// settings-less Voice round-trips identically (this used to live in
	// wirenpc.npcSpecFromAgent; it belongs with the canonical mapper now).
	if string(v.Settings) == "null" {
		v.Settings = nil
	}
	return v, nil
}

// VoiceToJSON encodes a [tts.Voice] into the canonical voice JSONB [VoiceFromJSON]
// reads back — the write counterpart, so both directions share one shape.
func VoiceToJSON(v tts.Voice) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("storage: encode voice: %w", err)
	}
	return b, nil
}
