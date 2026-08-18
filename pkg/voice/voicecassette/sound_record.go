//go:build record

package voicecassette

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen/elevenlabs"
	"gopkg.in/yaml.v3"
)

// LoadSound in -tags=record builds returns a [SoundRecorder] that proxies
// every generation to a live ElevenLabs client (ELEVENLABS_API_KEY supplies
// credentials), captures the request hash + response metadata, DISCARDS the
// audio bytes from the cassette (the TTS stub-cassette policy — the caller
// still receives them, so the test under record mode exercises real provider
// output), and rewrites tests/voice-cassettes/<name>.yaml at test cleanup.
// Any existing cassette's Notes and leading header comments are preserved
// with an idempotent dated provenance line.
func LoadSound(t *testing.T, name string) soundgen.Generator {
	t.Helper()
	existing, _ := loadSoundCassetteFromDisk(t, name, false)
	r := &SoundRecorder{
		name:     name,
		client:   elevenlabs.New(""),
		existing: existing,
	}
	t.Cleanup(func() {
		if err := r.write(); err != nil {
			t.Fatalf("voicecassette.LoadSound(%q): record write: %v", name, err)
		}
	})
	return r
}

// SoundRecorder is the -tags=record counterpart to [SoundGenerator]: it
// forwards every call to a live [elevenlabs.Client], returns the real result
// to the caller, and appends a keyed [SoundGeneration] so the cassette can be
// rewritten at cleanup.
type SoundRecorder struct {
	name     string
	client   *elevenlabs.Client
	existing SoundCassette

	mu          sync.Mutex
	generations []SoundGeneration
	// recModels names the models that produced the recorded bytes for the
	// provenance stamp (record_notes.go invariant: the stamp never lies).
	recModels map[string]bool
}

// GenerateSting implements [soundgen.Generator] by live forward + capture.
func (r *SoundRecorder) GenerateSting(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	res, err := r.client.GenerateSting(ctx, req)
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("voicecassette: SoundRecorder live GenerateSting for cassette %q: %w", r.name, err)
	}
	r.capture(soundKindSting, req, res)
	return res, nil
}

// ComposeMusic implements [soundgen.Generator] by live forward + capture.
func (r *SoundRecorder) ComposeMusic(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	res, err := r.client.ComposeMusic(ctx, req)
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("voicecassette: SoundRecorder live ComposeMusic for cassette %q: %w", r.name, err)
	}
	r.capture(soundKindMusic, req, res)
	return res, nil
}

func (r *SoundRecorder) capture(kind string, req soundgen.Request, res soundgen.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generations = append(r.generations, SoundGeneration{
		RequestHash: HashSoundRequest(kind, res.Model, req),
		Kind:        kind,
		Model:       res.Model,
		ContentType: res.ContentType,
	})
	if r.recModels == nil {
		r.recModels = map[string]bool{}
	}
	r.recModels[res.Model] = true
}

// write serialises the captured generations to
// tests/voice-cassettes/<name>.yaml, preserving the existing Notes (idempotent
// dated provenance) and re-prepending the hand-authored header block
// yaml.Marshal drops. A no-op if no generation was captured, so recording a
// test that never dispatched cannot clobber a fixture.
func (r *SoundRecorder) write() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.generations) == 0 {
		return nil
	}
	models := ""
	for m := range r.recModels {
		if models != "" {
			models += "+"
		}
		models += m
	}
	out := SoundCassette{
		Generations: r.generations,
		Notes:       appendProvenance(r.existing.Notes, "ElevenLabs", models),
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal cassette: %w", err)
	}
	data := append([]byte(leadingComment(r.name)), body...)
	path := filepath.Join(cassettesDir(), r.name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
