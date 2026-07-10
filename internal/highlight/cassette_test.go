package highlight

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fixtureLine is one scripted transcript final.
type fixtureLine struct {
	speaker string
	text    string
}

// dragonFixture is the demo transcript (#307 "Demo / verification"): a mundane
// stretch (corridor exploration) followed by one obviously epic moment (a natural
// twenty felling the dragon). With #305's ClassifyEvery=6 / ConfirmWindows=2 the
// two windows spanning the epic beat both score high and confirm exactly ONE
// trigger; the mundane opener scores low.
func dragonFixture() []fixtureLine {
	return []fixtureLine{
		{"gm", "You make your way down the damp stone corridor, torches guttering."},
		{"alice", "I check the walls for any hidden levers or traps."},
		{"gm", "You find nothing but moss and old cobwebs."},
		{"bob", "Boring. Let's keep moving toward the big doors ahead."},
		{"gm", "The corridor opens into a vast cavern."},
		{"alice", "We light a second torch and step inside carefully."},
		{"gm", "A colossal red dragon uncoils from the gold hoard, eyes blazing."},
		{"bob", "Oh gods. I draw my greatsword and charge the dragon!"},
		{"gm", "Roll your attack."},
		{"bob", "I rolled a natural twenty! A critical hit on the dragon!"},
		{"gm", "The blade sinks deep; the dragon roars in agony and staggers."},
		{"alice", "Incredible! That is the best hit of the whole campaign!"},
		{"gm", "The dragon collapses, shaking the cavern to its foundations."},
		{"bob", "We did it! We actually slew the dragon!"},
		{"alice", "For the tale-tellers: the night we felled the great wyrm."},
		{"gm", "Gold and firelight glitter across the fallen beast."},
		{"bob", "I raise my sword and let out a victory roar."},
		{"alice", "A moment we will never forget."},
	}
}

const dragonCassette = "llm-highlight-dragon"

// fixtureBase is the fixed clock the fixture stamps finals from (deterministic
// prompt hashes AND trigger ranges).
var fixtureBase = time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)

// fixtureConfig is the #305 detector tuning used for the fixture (real defaults).
func fixtureConfig() Config {
	return Config{
		ClassifyEvery:  6,
		Cooldown:       120 * time.Second,
		Bar:            8.0,
		ConfirmWindows: 2,
		MaxCandidates:  10,
		Lead:           15 * time.Second,
		Tail:           5 * time.Second,
	}
}

// publishFixture publishes the fixture finals SERIALIZED at their scripted times.
func publishFixture(t *testing.T, d *Detector, bus *voiceevent.Bus, lines []fixtureLine) {
	t.Helper()
	for i, l := range lines {
		bus.Publish(voiceevent.STTFinal{
			At:        fixtureBase.Add(time.Duration(i) * 2 * time.Second),
			Text:      l.text,
			SpeakerID: l.speaker,
		})
		select {
		case <-d.handled:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out handling fixture final %d", i)
		}
	}
}

// runFixture runs the dragon fixture through a detector backed by the given
// provider and returns the triggers the sink saw.
func runFixture(t *testing.T, prov llm.Provider) []Trigger {
	t.Helper()
	bus := voiceevent.NewBus()
	sink := &fakeSink{}
	snap := func(from, to time.Time) tape.Snapshot { return tape.Snapshot{From: from, To: to} }
	clk := &testClock{t: fixtureBase}
	d := newDetector(prov, "", snap, sink, allowGate{}, nil, nil, fixtureConfig())
	d.now = clk.now
	d.handled = make(chan struct{}, 1)
	d.start(bus)
	defer d.Close()
	publishFixture(t, d, bus, dragonFixture())
	return sink.all()
}

// TestDragonFixtureCassette (TEST 10) is the demo proof: replaying the recorded
// classifier over the fixture, exactly ONE trigger fires, over the epic moment's
// window±Lead/Tail range.
func TestDragonFixtureCassette(t *testing.T) {
	prov := voicecassette.LoadLLM(t, dragonCassette)
	trigs := runFixture(t, prov)
	if len(trigs) != 1 {
		t.Fatalf("trigger count = %d, want exactly 1", len(trigs))
	}
	tr := trigs[0]
	// Confirmed on the 18th final (index 17): At = base + 34s.
	wantAt := fixtureBase.Add(34 * time.Second)
	if !tr.At.Equal(wantAt) {
		t.Errorf("trigger At = %v, want %v (the confirming final)", tr.At, wantAt)
	}
	if !tr.From.Equal(wantAt.Add(-15*time.Second)) || !tr.To.Equal(wantAt.Add(5*time.Second)) {
		t.Errorf("range = (%v,%v), want (At-15s, At+5s)", tr.From, tr.To)
	}
	if tr.Score < 8.0 {
		t.Errorf("Score = %v, want >= Bar 8.0", tr.Score)
	}
}

// TestClassifierCassetteDeterminism (TEST 9): the same prompts replay the same
// verdicts, so two independent runs over the fixture produce byte-identical
// triggers (ADR-0021).
func TestClassifierCassetteDeterminism(t *testing.T) {
	a := runFixture(t, voicecassette.LoadLLM(t, dragonCassette))
	b := runFixture(t, voicecassette.LoadLLM(t, dragonCassette))
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("trigger counts = %d,%d, want 1,1", len(a), len(b))
	}
	if a[0].Score != b[0].Score || a[0].Excerpt != b[0].Excerpt || a[0].Reason != b[0].Reason ||
		!a[0].At.Equal(b[0].At) || !a[0].From.Equal(b[0].From) || !a[0].To.Equal(b[0].To) {
		t.Errorf("non-deterministic replay:\n a=%+v\n b=%+v", a[0], b[0])
	}
}

// ---- cassette generation (run manually, not in CI) -------------------------

// recordingProvider scores each classifier prompt by its content (epic if it
// mentions the natural twenty) and records the (prompt hash, response) exchange,
// so [TestGenerateDragonCassette] can author the replay cassette without a live
// LLM. It replays its own response so the detector runs end-to-end during capture.
type recordingProvider struct {
	mu        sync.Mutex
	exchanges []voicecassette.LLMExchange
}

func (p *recordingProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	var user string
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			user = m.Text
		}
	}
	resp := `{"score": 2.0, "excerpt": "the party explores the corridor", "reason": "routine exploration"}`
	if strings.Contains(strings.ToLower(user), "natural twenty") {
		resp = `{"score": 9.5, "excerpt": "I rolled a natural twenty! A critical hit on the dragon!", "reason": "a critical hit felling the dragon"}`
	}
	p.mu.Lock()
	p.exchanges = append(p.exchanges, voicecassette.LLMExchange{
		PromptHash: voicecassette.HashLLMRequest(req),
		Text:       resp,
		StopReason: "end_turn",
	})
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: resp}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// TestGenerateDragonCassette writes tests/voice-cassettes/llm-highlight-dragon.yaml
// from the fixture prompts. It is skipped unless GEN_HL_CASSETTE=1, so CI replays
// the committed cassette (a prompt change misses its hash and fails, ADR-0021);
// rerun with the env var set to regenerate after a deliberate prompt change.
func TestGenerateDragonCassette(t *testing.T) {
	if os.Getenv("GEN_HL_CASSETTE") == "" {
		t.Skip("set GEN_HL_CASSETTE=1 to regenerate the highlight cassette")
	}
	rec := &recordingProvider{}
	trigs := runFixture(t, rec)
	if len(trigs) != 1 {
		t.Fatalf("generation fixture produced %d triggers, want 1 (fix the fixture)", len(trigs))
	}
	cass := voicecassette.LLMCassette{
		Notes:     "Hand-authored highlight classifier fixture (#307): mundane corridor + one natural-twenty dragon kill. Not a live recording; responses are the fixture's scoring rule.",
		Exchanges: rec.exchanges,
	}
	data, err := yaml.Marshal(cass)
	if err != nil {
		t.Fatalf("marshal cassette: %v", err)
	}
	path := filepath.Join(repoCassettesDir(t), dragonCassette+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	t.Logf("wrote %s (%d exchanges)", path, len(rec.exchanges))
}

// repoCassettesDir locates tests/voice-cassettes/ by walking up to go.mod (the same
// trick voicecassette uses internally).
func repoCassettesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "tests", "voice-cassettes")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
