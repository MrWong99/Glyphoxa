package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// --- transcriptToMessages ---

func TestTranscriptToMessages(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		entries []memory.TranscriptEntry
		want    []llm.Message
	}{
		{
			name:    "empty entries",
			entries: nil,
			want:    []llm.Message{},
		},
		{
			name: "player entry mapped to user role",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Alice", Text: "Hello!", Timestamp: ts},
			},
			want: []llm.Message{
				{Role: "user", Name: "Alice", Content: "Hello!"},
			},
		},
		{
			name: "NPC entry mapped to assistant role",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Greymantle", NPCID: "npc-1", Text: "Greetings, traveler.", Timestamp: ts},
			},
			want: []llm.Message{
				{Role: "assistant", Name: "Greymantle", Content: "Greetings, traveler."},
			},
		},
		{
			name: "mixed entries preserve order",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Alice", Text: "Hi", Timestamp: ts},
				{SpeakerName: "Greymantle", NPCID: "npc-1", Text: "Welcome", Timestamp: ts.Add(time.Second)},
				{SpeakerName: "Bob", Text: "Hey there", Timestamp: ts.Add(2 * time.Second)},
			},
			want: []llm.Message{
				{Role: "user", Name: "Alice", Content: "Hi"},
				{Role: "assistant", Name: "Greymantle", Content: "Welcome"},
				{Role: "user", Name: "Bob", Content: "Hey there"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := transcriptToMessages(tt.entries)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role {
					t.Errorf("[%d].Role = %q, want %q", i, got[i].Role, tt.want[i].Role)
				}
				if got[i].Name != tt.want[i].Name {
					t.Errorf("[%d].Name = %q, want %q", i, got[i].Name, tt.want[i].Name)
				}
				if got[i].Content != tt.want[i].Content {
					t.Errorf("[%d].Content = %q, want %q", i, got[i].Content, tt.want[i].Content)
				}
			}
		})
	}
}

// --- formatTranscript ---

func TestFormatTranscript(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 3, 15, 14, 30, 45, 0, time.UTC)

	t.Run("single entry", func(t *testing.T) {
		t.Parallel()
		entries := []memory.TranscriptEntry{
			{SpeakerName: "Alice", Text: "Hello world", Timestamp: ts},
		}
		got := formatTranscript(entries)
		want := "**[14:30:45] Alice:** Hello world\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("multiple entries", func(t *testing.T) {
		t.Parallel()
		entries := []memory.TranscriptEntry{
			{SpeakerName: "Alice", Text: "Hi", Timestamp: ts},
			{SpeakerName: "Bob", Text: "Hey", Timestamp: ts.Add(time.Second)},
		}
		got := formatTranscript(entries)
		if !strings.Contains(got, "**[14:30:45] Alice:** Hi\n") {
			t.Errorf("missing Alice line in %q", got)
		}
		if !strings.Contains(got, "**[14:30:46] Bob:** Hey\n") {
			t.Errorf("missing Bob line in %q", got)
		}
	})

	t.Run("empty entries", func(t *testing.T) {
		t.Parallel()
		got := formatTranscript(nil)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("long transcript is truncated", func(t *testing.T) {
		t.Parallel()
		// Build entries large enough to exceed maxEmbedDescriptionLen-100 = 3996 chars.
		var entries []memory.TranscriptEntry
		longText := strings.Repeat("x", 200)
		for i := 0; i < 30; i++ {
			entries = append(entries, memory.TranscriptEntry{
				SpeakerName: "Speaker",
				Text:        longText,
				Timestamp:   ts.Add(time.Duration(i) * time.Second),
			})
		}
		got := formatTranscript(entries)
		if len(got) > maxEmbedDescriptionLen-100 {
			t.Errorf("len(result) = %d, want <= %d", len(got), maxEmbedDescriptionLen-100)
		}
		if !strings.HasSuffix(got, "*... (truncated)*") {
			t.Errorf("truncated result should end with truncation marker, got suffix %q",
				got[len(got)-30:])
		}
	})
}

// --- gatewayTranscriptToMessages ---

func TestGatewayTranscriptToMessages(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		entries []memory.TranscriptEntry
		want    []llm.Message
	}{
		{
			name:    "empty entries",
			entries: nil,
			want:    []llm.Message{},
		},
		{
			name: "player is user, NPC is assistant",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Player1", Text: "Attack!", Timestamp: ts},
				{SpeakerName: "Goblin", NPCID: "gob-1", Text: "Ouch!", Timestamp: ts.Add(time.Second)},
			},
			want: []llm.Message{
				{Role: "user", Name: "Player1", Content: "Attack!"},
				{Role: "assistant", Name: "Goblin", Content: "Ouch!"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gatewayTranscriptToMessages(tt.entries)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role {
					t.Errorf("[%d].Role = %q, want %q", i, got[i].Role, tt.want[i].Role)
				}
				if got[i].Name != tt.want[i].Name {
					t.Errorf("[%d].Name = %q, want %q", i, got[i].Name, tt.want[i].Name)
				}
				if got[i].Content != tt.want[i].Content {
					t.Errorf("[%d].Content = %q, want %q", i, got[i].Content, tt.want[i].Content)
				}
			}
		})
	}
}

// --- gatewayFormatTranscript ---

func TestGatewayFormatTranscript(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 6, 1, 9, 15, 0, 0, time.UTC)

	t.Run("formats correctly", func(t *testing.T) {
		t.Parallel()
		entries := []memory.TranscriptEntry{
			{SpeakerName: "Alice", Text: "Hello", Timestamp: ts},
		}
		got := gatewayFormatTranscript(entries)
		want := "**[09:15:00] Alice:** Hello\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("truncates long output", func(t *testing.T) {
		t.Parallel()
		var entries []memory.TranscriptEntry
		longText := strings.Repeat("y", 200)
		for i := 0; i < 30; i++ {
			entries = append(entries, memory.TranscriptEntry{
				SpeakerName: "Speaker",
				Text:        longText,
				Timestamp:   ts.Add(time.Duration(i) * time.Second),
			})
		}
		got := gatewayFormatTranscript(entries)
		if len(got) > maxEmbedDescriptionLen-100 {
			t.Errorf("len(result) = %d, want <= %d", len(got), maxEmbedDescriptionLen-100)
		}
		if !strings.HasSuffix(got, "*... (truncated)*") {
			t.Errorf("truncated result should end with truncation marker")
		}
	})
}

// --- buildGatewayRecapEmbeds ---

func TestBuildGatewayRecapEmbeds(t *testing.T) {
	t.Parallel()

	inlineTrue := true
	fields := []discord.EmbedField{
		{Name: "Status", Value: "Active", Inline: &inlineTrue},
	}

	t.Run("empty summary returns single embed with fields only", func(t *testing.T) {
		t.Parallel()
		embeds := buildGatewayRecapEmbeds(fields, "")
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Title != "Session Recap" {
			t.Errorf("Title = %q, want %q", embeds[0].Title, "Session Recap")
		}
		if embeds[0].Description != "" {
			t.Errorf("Description = %q, want empty", embeds[0].Description)
		}
		if len(embeds[0].Fields) != len(fields) {
			t.Errorf("Fields count = %d, want %d", len(embeds[0].Fields), len(fields))
		}
		if embeds[0].Footer == nil || embeds[0].Footer.Text != "Session recap" {
			t.Error("expected footer with 'Session recap'")
		}
		if embeds[0].Timestamp == nil {
			t.Error("expected non-nil Timestamp")
		}
		if embeds[0].Color != recapColor {
			t.Errorf("Color = %d, want %d", embeds[0].Color, recapColor)
		}
	})

	t.Run("short summary fits in single embed", func(t *testing.T) {
		t.Parallel()
		summary := "A brief adventure happened."
		embeds := buildGatewayRecapEmbeds(fields, summary)
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Description != summary {
			t.Errorf("Description = %q, want %q", embeds[0].Description, summary)
		}
		if embeds[0].Title != "Session Recap" {
			t.Errorf("Title = %q, want %q", embeds[0].Title, "Session Recap")
		}
		if len(embeds[0].Fields) != len(fields) {
			t.Errorf("Fields count = %d, want %d", len(embeds[0].Fields), len(fields))
		}
	})

	t.Run("long summary splits across multiple embeds", func(t *testing.T) {
		t.Parallel()
		// Summary that requires 3 embeds: 4096 + 4096 + remainder.
		summary := strings.Repeat("a", maxEmbedDescriptionLen*2+100)
		embeds := buildGatewayRecapEmbeds(fields, summary)

		if len(embeds) < 3 {
			t.Fatalf("len = %d, want >= 3", len(embeds))
		}

		// First embed has title and fields.
		if embeds[0].Title != "Session Recap" {
			t.Errorf("first embed Title = %q, want %q", embeds[0].Title, "Session Recap")
		}
		if len(embeds[0].Fields) != len(fields) {
			t.Errorf("first embed Fields count = %d, want %d", len(embeds[0].Fields), len(fields))
		}

		// Middle embeds have no title and no fields.
		for i := 1; i < len(embeds)-1; i++ {
			if embeds[i].Title != "" {
				t.Errorf("embed[%d] Title = %q, want empty", i, embeds[i].Title)
			}
			if len(embeds[i].Fields) != 0 {
				t.Errorf("embed[%d] Fields count = %d, want 0", i, len(embeds[i].Fields))
			}
		}

		// Last embed has footer and timestamp.
		last := embeds[len(embeds)-1]
		if last.Footer == nil || last.Footer.Text != "Session recap" {
			t.Error("last embed should have footer 'Session recap'")
		}
		if last.Timestamp == nil {
			t.Error("last embed should have non-nil Timestamp")
		}

		// First embed should NOT have footer (only last gets it).
		if embeds[0].Footer != nil {
			t.Error("first embed should not have footer when summary is split")
		}

		// All embeds should have the recap color.
		for i, emb := range embeds {
			if emb.Color != recapColor {
				t.Errorf("embed[%d].Color = %d, want %d", i, emb.Color, recapColor)
			}
		}

		// Verify total description content equals the original summary.
		var total strings.Builder
		for _, emb := range embeds {
			total.WriteString(emb.Description)
		}
		if total.String() != summary {
			t.Errorf("concatenated descriptions length = %d, want %d", total.Len(), len(summary))
		}
	})

	t.Run("exactly 4096 chars fits single embed", func(t *testing.T) {
		t.Parallel()
		summary := strings.Repeat("b", maxEmbedDescriptionLen)
		embeds := buildGatewayRecapEmbeds(fields, summary)
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Description != summary {
			t.Error("description should equal summary for exactly maxEmbedDescriptionLen chars")
		}
	})

	t.Run("4097 chars splits into two embeds", func(t *testing.T) {
		t.Parallel()
		summary := strings.Repeat("c", maxEmbedDescriptionLen+1)
		embeds := buildGatewayRecapEmbeds(fields, summary)
		if len(embeds) != 2 {
			t.Fatalf("len = %d, want 2", len(embeds))
		}
		if len(embeds[0].Description) != maxEmbedDescriptionLen {
			t.Errorf("first embed description len = %d, want %d", len(embeds[0].Description), maxEmbedDescriptionLen)
		}
		if len(embeds[1].Description) != 1 {
			t.Errorf("second embed description len = %d, want 1", len(embeds[1].Description))
		}
	})

	t.Run("nil fields works", func(t *testing.T) {
		t.Parallel()
		embeds := buildGatewayRecapEmbeds(nil, "summary text")
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Description != "summary text" {
			t.Errorf("Description = %q, want %q", embeds[0].Description, "summary text")
		}
		if embeds[0].Fields != nil {
			t.Errorf("Fields should be nil, got %d fields", len(embeds[0].Fields))
		}
	})

	t.Run("empty fields and empty summary", func(t *testing.T) {
		t.Parallel()
		embeds := buildGatewayRecapEmbeds([]discord.EmbedField{}, "")
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Description != "" {
			t.Errorf("Description = %q, want empty", embeds[0].Description)
		}
	})
}

// --- RecapCommands.buildRecapEmbeds ---

func TestRecapCommands_BuildRecapEmbeds(t *testing.T) {
	t.Parallel()

	rc := &RecapCommands{}

	inlineTrue := true
	fields := []discord.EmbedField{
		{Name: "Campaign", Value: "Test", Inline: &inlineTrue},
	}

	t.Run("empty summary", func(t *testing.T) {
		t.Parallel()
		embeds := rc.buildRecapEmbeds(fields, "")
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Title != "Session Recap" {
			t.Errorf("Title = %q, want %q", embeds[0].Title, "Session Recap")
		}
		if embeds[0].Description != "" {
			t.Errorf("Description = %q, want empty", embeds[0].Description)
		}
		if len(embeds[0].Fields) != 1 {
			t.Errorf("Fields count = %d, want 1", len(embeds[0].Fields))
		}
	})

	t.Run("short summary", func(t *testing.T) {
		t.Parallel()
		embeds := rc.buildRecapEmbeds(fields, "Brief recap.")
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
		if embeds[0].Description != "Brief recap." {
			t.Errorf("Description = %q, want %q", embeds[0].Description, "Brief recap.")
		}
	})

	t.Run("long summary splits", func(t *testing.T) {
		t.Parallel()
		summary := strings.Repeat("z", maxEmbedDescriptionLen+500)
		embeds := rc.buildRecapEmbeds(fields, summary)
		if len(embeds) < 2 {
			t.Fatalf("len = %d, want >= 2", len(embeds))
		}

		// First embed has title and fields.
		if embeds[0].Title != "Session Recap" {
			t.Errorf("first Title = %q, want %q", embeds[0].Title, "Session Recap")
		}
		if len(embeds[0].Fields) != 1 {
			t.Errorf("first Fields count = %d, want 1", len(embeds[0].Fields))
		}

		// Last embed has footer.
		last := embeds[len(embeds)-1]
		if last.Footer == nil || last.Footer.Text != "Session recap" {
			t.Error("last embed missing footer")
		}

		// Verify total content.
		var total strings.Builder
		for _, emb := range embeds {
			total.WriteString(emb.Description)
		}
		if total.String() != summary {
			t.Errorf("total len = %d, want %d", total.Len(), len(summary))
		}
	})

	t.Run("exactly max len fits single embed", func(t *testing.T) {
		t.Parallel()
		summary := strings.Repeat("w", maxEmbedDescriptionLen)
		embeds := rc.buildRecapEmbeds(fields, summary)
		if len(embeds) != 1 {
			t.Fatalf("len = %d, want 1", len(embeds))
		}
	})
}

// --- Single-entry edge cases for transcript functions ---

func TestTranscriptToMessages_SingleNPC(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []memory.TranscriptEntry{
		{SpeakerName: "Narrator", NPCID: "narrator-1", Text: "The story begins...", Timestamp: ts},
	}

	got := transcriptToMessages(entries)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Role != "assistant" {
		t.Errorf("Role = %q, want %q", got[0].Role, "assistant")
	}
}

func TestGatewayTranscriptToMessages_AllNPCs(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []memory.TranscriptEntry{
		{SpeakerName: "NPC1", NPCID: "n1", Text: "Hello", Timestamp: ts},
		{SpeakerName: "NPC2", NPCID: "n2", Text: "Greetings", Timestamp: ts.Add(time.Second)},
	}

	got := gatewayTranscriptToMessages(entries)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for i, msg := range got {
		if msg.Role != "assistant" {
			t.Errorf("[%d].Role = %q, want %q", i, msg.Role, "assistant")
		}
	}
}

func TestFormatTranscript_TimezoneFormatting(t *testing.T) {
	t.Parallel()

	// Verify timestamps with leading zeros are formatted correctly.
	ts := time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)
	entries := []memory.TranscriptEntry{
		{SpeakerName: "Player", Text: "Early morning", Timestamp: ts},
	}
	got := formatTranscript(entries)
	want := "**[01:02:03] Player:** Early morning\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- RecapConfig struct fields ---

func TestRecapConfig_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	cfg := RecapConfig{
		Perms: perms,
	}

	if cfg.Perms != perms {
		t.Error("Perms not set correctly")
	}
	if cfg.Bot != nil {
		t.Error("expected nil Bot")
	}
	if cfg.SessionMgr != nil {
		t.Error("expected nil SessionMgr")
	}
	if cfg.SessionStore != nil {
		t.Error("expected nil SessionStore")
	}
	if cfg.Summariser != nil {
		t.Error("expected nil Summariser")
	}
}

// --- RecapCommands struct fields ---

func TestRecapCommands_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	rc := &RecapCommands{
		perms: perms,
	}

	if rc.perms != perms {
		t.Error("perms not set correctly")
	}
	if rc.sessionMgr != nil {
		t.Error("expected nil sessionMgr")
	}
	if rc.sessionStore != nil {
		t.Error("expected nil sessionStore")
	}
	if rc.summariser != nil {
		t.Error("expected nil summariser")
	}
}

// --- GatewayRecapConfig struct fields ---

func TestGatewayRecapConfig_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("dm-role")
	cfg := GatewayRecapConfig{
		Perms: perms,
	}

	if cfg.Perms != perms {
		t.Error("Perms not set correctly")
	}
	if cfg.GatewayBot != nil {
		t.Error("expected nil GatewayBot")
	}
	if cfg.Ctrl != nil {
		t.Error("expected nil Ctrl")
	}
	if cfg.NPCCtrl != nil {
		t.Error("expected nil NPCCtrl")
	}
	if cfg.SessionStore != nil {
		t.Error("expected nil SessionStore")
	}
	if cfg.Summariser != nil {
		t.Error("expected nil Summariser")
	}
}

// --- GatewayRecapCommands struct fields ---

func TestGatewayRecapCommands_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	rc := &GatewayRecapCommands{
		perms: perms,
	}

	if rc.perms != perms {
		t.Error("perms not set correctly")
	}
	if rc.ctrl != nil {
		t.Error("expected nil ctrl")
	}
	if rc.npcCtrl != nil {
		t.Error("expected nil npcCtrl")
	}
	if rc.sessionStore != nil {
		t.Error("expected nil sessionStore")
	}
	if rc.summariser != nil {
		t.Error("expected nil summariser")
	}
}

func TestGatewayFormatTranscript_Empty(t *testing.T) {
	t.Parallel()

	got := gatewayFormatTranscript(nil)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}
