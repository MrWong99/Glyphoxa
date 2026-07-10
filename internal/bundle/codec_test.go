package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func fullBundle() *Bundle {
	end := time.Date(2026, 7, 10, 21, 0, 0, 0, time.UTC)
	reason := "gm_ended"
	return &Bundle{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC),
		Campaign: Campaign{
			Name:     "The Prancing Pony",
			System:   "D&D 5e",
			Language: "de",
			Agents: []Agent{
				{
					ID:          "a1",
					Role:        "butler",
					Name:        "Glyphoxa",
					Title:       "Majordomo",
					Persona:     "Helpful.",
					Voice:       json.RawMessage(`{"provider_voice":"barb"}`),
					AddressOnly: true,
					Aliases:     []string{"Butler"},
					Grants: []Grant{
						{ToolName: "roll_dice", Config: json.RawMessage(`{"max":20}`)},
					},
				},
			},
			Nodes: []Node{
				{ID: "n1", Type: "npc", Name: "Barliman", Body: "Innkeeper.", GMPrivate: true, AgentID: "a1"},
			},
			Edges: []Edge{
				{From: "n1", To: "a1", Type: "is"},
			},
			Characters: []Character{
				{Name: "Frodo", Aliases: []string{"Mr. Underhill"}, DiscordUserID: "123"},
			},
			History: &History{
				Sessions: []Session{
					{
						ID:        "s1",
						StartedAt: time.Date(2026, 7, 10, 19, 0, 0, 0, time.UTC),
						EndedAt:   &end,
						Status:    "ended",
						LineCount: 1,
						EndReason: &reason,
						Lines: []Line{
							{LineID: "l1", Seq: 1, Who: "player", Tag: "Frodo", Kind: "speech", TS: end, Text: "Hi", SpeakerDiscordUserID: "123"},
						},
						Chunks: []Chunk{
							{Content: "Hi there", SpeakerDiscordUserIDs: []string{"123"}, ParticipatedAgentIDs: []string{"a1"}, StartedAt: end},
						},
					},
				},
			},
		},
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	orig := fullBundle()
	var buf bytes.Buffer
	if err := Encode(&buf, orig); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Compare via canonical compact JSON: MarshalIndent reindents embedded
	// json.RawMessage (Voice, Grant.Config), which is a whitespace-only
	// difference, so DeepEqual on the raw bytes is too strict.
	if a, b := compact(t, orig), compact(t, got); a != b {
		t.Fatalf("round-trip mismatch:\norig=%s\ngot =%s", a, b)
	}
}

func compact(t *testing.T, b *Bundle) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDecodeAcceptsPlainJSON(t *testing.T) {
	orig := fullBundle()
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode plain JSON: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Fatal("plain-JSON decode mismatch")
	}
}

func TestCheckVersionErrors(t *testing.T) {
	if err := CheckVersion(FormatVersion); err != nil {
		t.Fatalf("current version should be ok, got %v", err)
	}
	if err := CheckVersion(FormatVersion + 1); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("want ErrNewerFormat, got %v", err)
	}
	if err := CheckVersion(0); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("want ErrUnsupportedFormat, got %v", err)
	}
}

func TestDecodeNewerVersionMessage(t *testing.T) {
	raw := []byte(`{"format_version":2,"exported_at":"2026-07-10T20:00:00Z","campaign":{"name":"x","system":"y","language":"de","agents":[]}}`)
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("want ErrNewerFormat, got %v", err)
	}
	msg := err.Error()
	if !bytes.Contains([]byte(msg), []byte("2")) || !bytes.Contains([]byte(msg), []byte("1")) {
		t.Fatalf("message must mention both versions, got %q", msg)
	}
}

func TestDecodeOlderVersionUnsupported(t *testing.T) {
	raw := []byte(`{"format_version":0,"exported_at":"2026-07-10T20:00:00Z","campaign":{"name":"x","system":"y","language":"de","agents":[]}}`)
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("want ErrUnsupportedFormat, got %v", err)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	raw := []byte(`{"format_version":1,"exported_at":"2026-07-10T20:00:00Z","campaign":{"name":"x","system":"y","language":"de","agents":[]},"secret_ciphertext":"boom"}`)
	_, err := Decode(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("expected unknown-field rejection")
	}
	if errors.Is(err, ErrNewerFormat) || errors.Is(err, ErrUnsupportedFormat) || errors.Is(err, ErrTooLarge) {
		t.Fatalf("unexpected sentinel for unknown field: %v", err)
	}
}

func TestDecodeTooLarge(t *testing.T) {
	orig := decodeLimit
	decodeLimit = 32
	defer func() { decodeLimit = orig }()
	raw := []byte(`{"format_version":1,"exported_at":"2026-07-10T20:00:00Z","campaign":{"name":"padpadpadpadpadpad","system":"y","language":"de","agents":[]}}`)
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestFilenameSlug(t *testing.T) {
	cases := map[string]string{
		"The Prancing Pony": "the-prancing-pony.glyphoxa.json.gz",
		"!!!":               "campaign.glyphoxa.json.gz",
		"":                  "campaign.glyphoxa.json.gz",
		"  Hello, World!  ": "hello-world.glyphoxa.json.gz",
	}
	for in, want := range cases {
		if got := Filename(in); got != want {
			t.Errorf("Filename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeHandWrittenMinimalBundle(t *testing.T) {
	raw := `{
  "format_version": 1,
  "exported_at": "2026-07-10T20:00:00Z",
  "campaign": {
    "name": "Tiny",
    "system": "D&D 5e",
    "language": "en",
    "agents": [
      {"id": "n1", "role": "npc", "name": "Bob"}
    ]
  }
}`
	b, err := Decode(bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatalf("Decode hand-written: %v", err)
	}
	if b.Campaign.Name != "Tiny" || len(b.Campaign.Agents) != 1 || b.Campaign.Agents[0].ID != "n1" {
		t.Fatalf("unexpected decode: %+v", b)
	}
}
