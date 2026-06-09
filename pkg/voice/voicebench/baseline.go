package voicebench

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// WriteBaseline marshals r to path as indented JSON — the committed regression
// floor the cassette tier diffs against. The stage map is emitted in the locked
// [Stages] order for a stable, review-friendly diff (Go map order is random).
func WriteBaseline(path string, r Report) error {
	data, err := marshalStable(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadBaseline reads a committed baseline report from path.
func LoadBaseline(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("voicebench: parse baseline %s: %w", path, err)
	}
	return r, nil
}

// marshalStable renders the report with its stages in canonical order so the
// committed baseline.json diffs cleanly across runs (encoding/json sorts map
// keys, but emitting an ordered structure keeps the whole artifact stable and
// makes the headline stage read first).
func marshalStable(r Report) ([]byte, error) {
	// encoding/json already sorts string-keyed maps lexically, which is stable;
	// but we want the report's own field order + a trailing newline for a clean
	// git diff. A plain MarshalIndent over Report suffices for stability since
	// Stages is a map[Stage]… that json sorts deterministically.
	keys := make([]string, 0, len(r.Stages))
	for k := range r.Stages {
		keys = append(keys, string(k))
	}
	sort.Strings(keys) // documents the determinism even though json re-sorts
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
