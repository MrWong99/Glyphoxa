package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		want     AttachmentFormat
	}{
		{"campaign.yaml", FormatYAML},
		{"campaign.yml", FormatYAML},
		{"CAMPAIGN.YAML", FormatYAML},
		{"world.json", FormatJSON},
		{"export.JSON", FormatJSON},
		{"readme.txt", FormatUnknown},
		{"image.png", FormatUnknown},
		{"noext", FormatUnknown},
		{"", FormatUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			t.Parallel()
			got := DetectFormat(tt.filename)
			if got != tt.want {
				t.Errorf("DetectFormat(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestAttachmentFormatString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format AttachmentFormat
		want   string
	}{
		{FormatYAML, "yaml"},
		{FormatJSON, "json"},
		{FormatUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.format.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstAttachment_NoAttachments(t *testing.T) {
	t.Parallel()

	data := discord.SlashCommandInteractionData{}
	if _, ok := FirstAttachment(data); ok {
		t.Error("expected no attachment, got one")
	}
}

func TestFirstAttachment_EmptyMap(t *testing.T) {
	t.Parallel()

	data := discord.SlashCommandInteractionData{
		Resolved: discord.ResolvedData{
			Attachments: map[snowflake.ID]discord.Attachment{},
		},
	}
	if _, ok := FirstAttachment(data); ok {
		t.Error("expected no attachment from empty map, got one")
	}
}

func TestFirstAttachment_SingleAttachment(t *testing.T) {
	t.Parallel()

	data := discord.SlashCommandInteractionData{
		Resolved: discord.ResolvedData{
			Attachments: map[snowflake.ID]discord.Attachment{
				snowflake.ID(1): {
					ID:       snowflake.ID(1),
					Filename: "campaign.yaml",
					Size:     1024,
					URL:      "https://cdn.example.com/campaign.yaml",
				},
			},
		},
	}

	att, ok := FirstAttachment(data)
	if !ok {
		t.Fatal("expected attachment, got none")
	}
	if att.Filename != "campaign.yaml" {
		t.Errorf("Filename = %q, want %q", att.Filename, "campaign.yaml")
	}
	if att.Size != 1024 {
		t.Errorf("Size = %d, want %d", att.Size, 1024)
	}
}

func TestFirstAttachment_MultipleAttachments(t *testing.T) {
	t.Parallel()

	data := discord.SlashCommandInteractionData{
		Resolved: discord.ResolvedData{
			Attachments: map[snowflake.ID]discord.Attachment{
				snowflake.ID(1): {
					ID:       snowflake.ID(1),
					Filename: "first.yaml",
					Size:     100,
				},
				snowflake.ID(2): {
					ID:       snowflake.ID(2),
					Filename: "second.json",
					Size:     200,
				},
				snowflake.ID(3): {
					ID:       snowflake.ID(3),
					Filename: "third.yml",
					Size:     300,
				},
			},
		},
	}

	att, ok := FirstAttachment(data)
	if !ok {
		t.Fatal("expected attachment from multi-attachment map, got none")
	}
	// Map iteration order is non-deterministic, but we should get one valid attachment.
	if att.Filename == "" {
		t.Error("returned attachment has empty filename")
	}
	if att.Size == 0 {
		t.Error("returned attachment has zero size")
	}
}

func TestAttachmentFormatString_UnknownValue(t *testing.T) {
	t.Parallel()

	// Test that an out-of-range format value still returns "unknown".
	format := AttachmentFormat(99)
	if got := format.String(); got != "unknown" {
		t.Errorf("String() for invalid format = %q, want %q", got, "unknown")
	}
}

func TestDownloadedAttachment_Fields(t *testing.T) {
	t.Parallel()

	da := &DownloadedAttachment{
		Filename: "world.json",
		Format:   FormatJSON,
		Size:     42,
	}
	if da.Filename != "world.json" {
		t.Errorf("Filename = %q, want %q", da.Filename, "world.json")
	}
	if da.Format != FormatJSON {
		t.Errorf("Format = %v, want %v", da.Format, FormatJSON)
	}
	if da.Size != 42 {
		t.Errorf("Size = %d, want %d", da.Size, 42)
	}
}
