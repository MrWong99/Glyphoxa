package voicecassette

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
	"gopkg.in/yaml.v3"
)

// SoundGeneration is one (request fingerprint, recorded metadata) pair in a
// [SoundCassette] — a single [soundgen.Generator] call (a sting or a Music
// composition, discriminated by Kind).
//
// Like the LLM cassette (and unlike the positional STT/TTS ones), generations
// are matched by RequestHash: the key is a sha256 over the kind, model, target
// duration, and prompt, so a prompt-derivation change misses and fails the
// test rather than silently replaying a stale asset. Like the TTS cassette,
// this is a STUB cassette (ADR-0021): the generated audio bytes are NOT
// pinned — megabytes of MP3 in git would be a departure — only the request
// that flowed into the provider plus the response's content type. Replay
// returns small deterministic stand-in bytes.
type SoundGeneration struct {
	// RequestHash is the hex sha256 of the rendered request (kind + model +
	// duration + prompt); see [HashSoundRequest].
	RequestHash string `yaml:"request_sha256"`

	// Kind is "sting" or "music" — which Generator method the request hit.
	Kind string `yaml:"kind"`

	// Model is the provider model id that produced the recorded response —
	// the metering attribution key the replay result carries.
	Model string `yaml:"model"`

	// ContentType is the recorded response's audio MIME type.
	ContentType string `yaml:"content_type"`
}

// SoundCassette is the on-disk record of a sound-generation scenario: a set of
// generations keyed by request hash. Its identity is its filename —
// LoadSound(t, "sound-foo") reads tests/voice-cassettes/sound-foo.yaml.
type SoundCassette struct {
	// Generations is the recorded (hash, metadata) set. Stored as a list (not
	// a map) so the YAML diff is stable and reviewable; replay indexes it by
	// hash on load.
	Generations []SoundGeneration `yaml:"generations"`

	// Notes is free-form provenance (provider, model, recording date). Not
	// load-bearing; survives round-trip for human reviewers.
	Notes string `yaml:"notes,omitempty"`
}

// Sound cassette kind tokens, matching the two [soundgen.Generator] methods.
const (
	soundKindSting = "sting"
	soundKindMusic = "music"
)

// SoundGenerator is a [soundgen.Generator] that replays a single
// [SoundCassette]. Each call hashes its request and replays the matching
// generation's metadata over deterministic stub bytes. A miss — no generation
// for the computed hash — returns an error pointing at the re-record workflow,
// so a prompt or duration change is caught, never silently passed.
type SoundGenerator struct {
	name   string
	byHash map[string]SoundGeneration
}

// loadSoundCassetteFromDisk reads tests/voice-cassettes/<name>.yaml and
// returns the decoded cassette. When mustExist is true (replay mode) every
// failure path — missing file, malformed YAML, empty generations, empty
// request hash — is fatal. When mustExist is false (record mode), a missing
// file yields (zero, false); a malformed existing file still fails so a
// corrupted fixture is never silently overwritten.
//
// One function instead of two so neither build configuration (default replay
// vs -tags=record) sees an unused helper — only one of [LoadSound]'s build-tag
// variants is compiled at a time.
func loadSoundCassetteFromDisk(t *testing.T, name string, mustExist bool) (SoundCassette, bool) {
	t.Helper()
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if !mustExist && os.IsNotExist(err) {
			return SoundCassette{}, false
		}
		t.Fatalf("voicecassette.LoadSound(%q): %v", name, err)
	}
	var c SoundCassette
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("voicecassette.LoadSound(%q): unmarshal: %v", name, err)
	}
	if mustExist {
		if len(c.Generations) == 0 {
			t.Fatalf("voicecassette.LoadSound(%q): cassette has no generations", name)
		}
		for i, g := range c.Generations {
			if g.RequestHash == "" {
				t.Fatalf("voicecassette.LoadSound(%q): generation %d has empty request_sha256", name, i)
			}
		}
	}
	return c, true
}

// indexSoundByHash builds the replay lookup from a cassette's generations.
func indexSoundByHash(c SoundCassette) map[string]SoundGeneration {
	m := make(map[string]SoundGeneration, len(c.Generations))
	for _, g := range c.Generations {
		m[g.RequestHash] = g
	}
	return m
}

// GenerateSting implements [soundgen.Generator] by replay.
func (g *SoundGenerator) GenerateSting(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	return g.replay(soundKindSting, req)
}

// ComposeMusic implements [soundgen.Generator] by replay.
func (g *SoundGenerator) ComposeMusic(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	return g.replay(soundKindMusic, req)
}

// replay hashes the request under kind, looks up the recorded generation, and
// returns its metadata over stub bytes. The model folded into the hash is the
// CASSETTE's recorded model for this kind (the replay generator has no live
// client to ask), so a model bump re-records rather than silently matching.
func (g *SoundGenerator) replay(kind string, req soundgen.Request) (soundgen.Result, error) {
	// Try every recorded generation of this kind: the hash embeds the model,
	// which replay only knows from the cassette itself.
	for _, gen := range g.byHash {
		if gen.Kind != kind {
			continue
		}
		if HashSoundRequest(kind, gen.Model, req) == gen.RequestHash {
			return soundgen.Result{
				Data:        soundStubBytes(gen.RequestHash),
				ContentType: gen.ContentType,
				Model:       gen.Model,
			}, nil
		}
	}
	// Miss: name a computed hash so the diff against the cassette is obvious.
	// Use the first same-kind recorded model as the candidate, else "".
	model := ""
	for _, gen := range g.byHash {
		if gen.Kind == kind {
			model = gen.Model
			break
		}
	}
	return soundgen.Result{}, fmt.Errorf(
		"voicecassette: no %s generation for request hash %s in cassette %q (%d recorded); re-record with -tags=record",
		kind, HashSoundRequest(kind, model, req), g.name, len(g.byHash),
	)
}

// soundStubBytes returns the small deterministic stand-in audio for a replayed
// generation (the TTS stub-cassette policy: real bytes are never pinned).
func soundStubBytes(hash string) []byte {
	return []byte("voicecassette-sound-stub:" + hash)
}

// HashSoundRequest returns the hex sha256 cassette key for one sound
// generation: kind + model + target duration + prompt, marshalled to canonical
// JSON (struct fields render in declaration order). Exported so the record
// path and test helpers compute the same fingerprint.
func HashSoundRequest(kind, model string, req soundgen.Request) string {
	h := struct {
		Kind       string `json:"kind"`
		Model      string `json:"model"`
		DurationMs int64  `json:"duration_ms"`
		Prompt     string `json:"prompt"`
	}{Kind: kind, Model: model, DurationMs: req.Duration.Milliseconds(), Prompt: req.Prompt}
	b, err := json.Marshal(h)
	if err != nil {
		// Plain strings and an int64 cannot fail to marshal; a panic here means
		// the hash shape itself changed incompatibly — fail loudly at the source
		// (the HashLLMRequest posture).
		panic(fmt.Sprintf("voicecassette: hash sound request: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
