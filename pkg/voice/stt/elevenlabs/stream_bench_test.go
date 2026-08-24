package elevenlabs

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// sttFrame is one recorded Scribe v2 realtime websocket frame plus the values
// readPump must read out of it. The fixtures are hand-authored from the frame
// shapes readPump switches on (message_type / text / error, see stream.go) and
// ADR-0042's message semantics: partial_transcript flows during speech, the
// local VAD's manual commit turns into committed_transcript (= STTFinal),
// insufficient_audio_activity is the empty utterance, and everything else is an
// error frame classified by routeErrorFrame.
type sttFrame struct {
	name      string
	raw       []byte
	wantType  string
	wantText  string
	wantError string
}

// sttFrames is the benchmark corpus: one frame of every shape readPump routes,
// with realistic TTRPG table speech (DE and EN, STT punctuation) rather than
// microstrings, so string decoding costs what it costs in production.
var sttFrames = []sttFrame{
	{
		name:     "session_started",
		raw:      []byte(`{"message_type":"session_started","session_id":"sess_01JYQ4Z8K2M7","expires_at":1786790400}`),
		wantType: msgSessionStarted,
	},
	{
		name:     "partial_transcript_short",
		raw:      []byte(`{"message_type":"partial_transcript","text":"Ich gehe zum","audio_start_ms":0,"audio_end_ms":940}`),
		wantType: msgPartialTranscript,
		wantText: "Ich gehe zum",
	},
	{
		name:     "partial_transcript_long",
		raw:      []byte(`{"message_type":"partial_transcript","text":"Okay, so we head back to the Rusty Flagon — wait, does Grimnir still owe us money?","audio_start_ms":0,"audio_end_ms":5120}`),
		wantType: msgPartialTranscript,
		wantText: "Okay, so we head back to the Rusty Flagon — wait, does Grimnir still owe us money?",
	},
	{
		name:     "committed_transcript",
		raw:      []byte(`{"message_type":"committed_transcript","text":"Wer ist eigentlich dieser Händler am Nordtor, und was verkauft er?","audio_start_ms":0,"audio_end_ms":4380,"language_code":"deu"}`),
		wantType: msgCommitted,
		wantText: "Wer ist eigentlich dieser Händler am Nordtor, und was verkauft er?",
	},
	{
		name:     "insufficient_audio_activity",
		raw:      []byte(`{"message_type":"insufficient_audio_activity","text":""}`),
		wantType: msgInsufficientAudio,
	},
	{
		name:      "commit_throttled",
		raw:       []byte(`{"message_type":"commit_throttled","error":"commit rate exceeded; retry after the current commit resolves"}`),
		wantType:  "commit_throttled",
		wantError: "commit rate exceeded; retry after the current commit resolves",
	},
	{
		name:      "fatal_error",
		raw:       []byte(`{"message_type":"quota_exceeded","error":"character quota exhausted for this workspace"}`),
		wantType:  "quota_exceeded",
		wantError: "character quota exhausted for this workspace",
	},
}

// sttDecodeStruct mirrors the anonymous struct readPump unmarshals every
// websocket frame into (stream.go). It is a hand-made copy — readPump's struct
// is anonymous, so no compiler-level binding to it exists — but the copy is NOT
// left to trust: TestSTTDecodeStruct_MatchesReadPump parses stream.go and fails
// if the two shapes drift apart.
type sttDecodeStruct struct {
	MessageType string `json:"message_type"`
	Text        string `json:"text"`
	Error       string `json:"error"`
}

// decodeSTTFrame is the decode readPump performs on every websocket frame, over
// the mirrored production struct shape.
func decodeSTTFrame(data []byte) (messageType, text, errText string, err error) {
	var msg sttDecodeStruct
	if err := json.Unmarshal(data, &msg); err != nil {
		return "", "", "", err
	}
	return msg.MessageType, msg.Text, msg.Error, nil
}

// structField is one field of a decode struct as either the source or
// reflection sees it: Go name, type expression, and struct tag.
type structField struct {
	Name string
	Type string
	Tag  string
}

// readPumpDecodeFields parses the given Go source and returns the fields of the
// anonymous struct declared as `var msg struct { ... }` inside readPump — the
// literal shape production decodes every Scribe frame into.
func readPumpDecodeFields(t *testing.T, src []byte) []structField {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "stream.go", src, 0)
	if err != nil {
		t.Fatalf("parse stream.go: %v", err)
	}

	var lit *ast.StructType
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "readPump" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "msg" {
				return true
			}
			if st, ok := vs.Type.(*ast.StructType); ok {
				lit = st
				return false
			}
			return true
		})
	}
	if lit == nil {
		t.Fatal("no `var msg struct{...}` found in readPump — has the decode site moved?")
	}

	var out []structField
	for _, f := range lit.Fields.List {
		typ := &strings.Builder{}
		if err := printer.Fprint(typ, token.NewFileSet(), f.Type); err != nil {
			t.Fatalf("print field type: %v", err)
		}
		tag := ""
		if f.Tag != nil {
			unquoted, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				t.Fatalf("unquote tag %s: %v", f.Tag.Value, err)
			}
			tag = unquoted
		}
		for _, name := range f.Names {
			out = append(out, structField{Name: name.Name, Type: typ.String(), Tag: tag})
		}
	}
	return out
}

// TestSTTDecodeStruct_MatchesReadPump is the drift guard for the benchmark's
// hand-made copy of readPump's anonymous decode struct. It reads stream.go,
// extracts the real struct literal, and asserts field-for-field (name, type,
// json tag) equality with [sttDecodeStruct]. If readPump's struct gains,
// renames, or retags a field, this fails — update sttDecodeStruct — instead of
// letting the benchmarks silently measure a stale shape.
func TestSTTDecodeStruct_MatchesReadPump(t *testing.T) {
	src, err := os.ReadFile("stream.go")
	if err != nil {
		t.Fatalf("read stream.go: %v", err)
	}
	want := readPumpDecodeFields(t, src)

	rt := reflect.TypeOf(sttDecodeStruct{})
	got := make([]structField, 0, rt.NumField())
	for i := range rt.NumField() {
		f := rt.Field(i)
		got = append(got, structField{Name: f.Name, Type: f.Type.String(), Tag: string(f.Tag)})
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("sttDecodeStruct has drifted from readPump's decode struct in stream.go\n got: %+v\nwant: %+v", got, want)
	}
}

// TestSTTFrameFixtures_DecodeToReadPumpFields keeps the benchmark corpus honest:
// every fixture must decode into exactly the message_type / text / error values
// readPump routes on. A fixture that stops decoding (or a decode struct that
// drifts from stream.go's) fails here rather than silently benchmarking a
// shape production never sees.
func TestSTTFrameFixtures_DecodeToReadPumpFields(t *testing.T) {
	for _, f := range sttFrames {
		t.Run(f.name, func(t *testing.T) {
			gotType, gotText, gotErr, err := decodeSTTFrame(f.raw)
			if err != nil {
				t.Fatalf("decode %s: %v", f.name, err)
			}
			if gotType != f.wantType {
				t.Errorf("message_type = %q, want %q", gotType, f.wantType)
			}
			if gotText != f.wantText {
				t.Errorf("text = %q, want %q", gotText, f.wantText)
			}
			if gotErr != f.wantError {
				t.Errorf("error = %q, want %q", gotErr, f.wantError)
			}
		})
	}
}

// BenchmarkReadPumpDecode measures the per-frame json.Unmarshal readPump runs on
// every Scribe v2 websocket frame (#609). One iteration decodes one frame,
// round-robin over the corpus, so the number reads as "per received frame".
//
// # Messages/second → CPU budget
//
// No live Voice Session capture was available when this benchmark landed, so the
// rate below is a documented estimate, not a measurement (live verification is a
// follow-up):
//
//   - Scribe v2 realtime emits partial_transcript while a player speaks. Realtime
//     STT partial cadence is ~100–250 ms, so assume the pessimistic end: ~10
//     frames/s per actively speaking player.
//   - ADR-0042 keeps local VAD as the endpointing authority with
//     commit_strategy "manual", so audio only streams while VAD says a player is
//     voiced — the stream is idle between utterances, and per Voice Session
//     roughly one player speaks at a time. Say ~10 frames/s per active session,
//     plus 1 committed_transcript per utterance (~1 every 5 s) — noise next to
//     the partials.
//   - A voice pod under the ADR-0057 claim plane hosts many sessions; take 20
//     concurrently-speaking sessions as a busy-pod ceiling → ~200 frames/s.
//
// At that ceiling the decode's CPU share is 200 × ns/op: a 1 µs/op decode is
// 200 µs/s ≈ 0.02 % of one core. Adoption of encoding/json/v2 here is therefore
// only worth filing if ns/op is orders of magnitude worse than that, or if
// allocs/op matter for GC pressure rather than CPU.
func BenchmarkReadPumpDecode(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, _, _, err := decodeSTTFrame(sttFrames[i%len(sttFrames)].raw); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkReadPumpDecodePartial isolates the single hottest frame shape: the
// partial_transcript that arrives ~10×/s per speaking player (every other frame
// shape is per-utterance or rarer).
func BenchmarkReadPumpDecodePartial(b *testing.B) {
	raw := sttFrames[2].raw // partial_transcript_long
	b.ReportAllocs()
	for b.Loop() {
		if _, _, _, err := decodeSTTFrame(raw); err != nil {
			b.Fatal(err)
		}
	}
}
