package discord

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

// noopResponder is a no-op InteractionResponderFunc for tests.
func noopResponder(_ discord.InteractionResponseType, _ discord.InteractionResponseData, _ ...rest.RequestOpt) error {
	return nil
}

// makeSlashCommandEvent creates an ApplicationCommandInteractionCreate event
// from a JSON representation of a SlashCommandInteractionData. This is the
// only way to populate unexported fields like `name`.
func makeSlashCommandEvent(t *testing.T, dataJSON string) *events.ApplicationCommandInteractionCreate {
	t.Helper()

	// Build a full ApplicationCommandInteraction JSON with type=2 (slash command).
	interactionJSON := `{
		"id": "1",
		"type": 2,
		"application_id": "100",
		"token": "test-token",
		"version": 1,
		"data": ` + dataJSON + `
	}`

	var interaction discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(interactionJSON), &interaction); err != nil {
		t.Fatalf("failed to unmarshal ApplicationCommandInteraction: %v", err)
	}

	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: interaction,
		Respond:                       noopResponder,
	}
}

// makeComponentEvent creates a ComponentInteractionCreate event from JSON.
func makeComponentEvent(t *testing.T, customID string) *events.ComponentInteractionCreate {
	t.Helper()

	interactionJSON := `{
		"id": "1",
		"type": 3,
		"application_id": "100",
		"token": "test-token",
		"version": 1,
		"data": {
			"component_type": 2,
			"custom_id": ` + `"` + customID + `"` + `
		},
		"message": {"id": "999", "channel_id": "888"}
	}`

	var interaction discord.ComponentInteraction
	if err := json.Unmarshal([]byte(interactionJSON), &interaction); err != nil {
		t.Fatalf("failed to unmarshal ComponentInteraction: %v", err)
	}

	return &events.ComponentInteractionCreate{
		ComponentInteraction: interaction,
		Respond:              noopResponder,
	}
}

// makeAutocompleteEvent creates an AutocompleteInteractionCreate event from JSON.
func makeAutocompleteEvent(t *testing.T, dataJSON string) *events.AutocompleteInteractionCreate {
	t.Helper()

	interactionJSON := `{
		"id": "1",
		"type": 4,
		"application_id": "100",
		"token": "test-token",
		"version": 1,
		"data": ` + dataJSON + `
	}`

	var interaction discord.AutocompleteInteraction
	if err := json.Unmarshal([]byte(interactionJSON), &interaction); err != nil {
		t.Fatalf("failed to unmarshal AutocompleteInteraction: %v", err)
	}

	return &events.AutocompleteInteractionCreate{
		AutocompleteInteraction: interaction,
		Respond:                 noopResponder,
	}
}

// makeModalEvent creates a ModalSubmitInteractionCreate event.
func makeModalEvent(t *testing.T, customID string) *events.ModalSubmitInteractionCreate {
	t.Helper()

	interactionJSON := `{
		"id": "1",
		"type": 5,
		"application_id": "100",
		"token": "test-token",
		"version": 1,
		"data": {
			"custom_id": "` + customID + `",
			"components": []
		}
	}`

	var interaction discord.ModalSubmitInteraction
	if err := json.Unmarshal([]byte(interactionJSON), &interaction); err != nil {
		t.Fatalf("failed to unmarshal ModalSubmitInteraction: %v", err)
	}

	return &events.ModalSubmitInteractionCreate{
		ModalSubmitInteraction: interaction,
		Respond:                noopResponder,
	}
}

func TestCommandRouter_RegisterAutocomplete(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterAutocomplete("ping", func(e *events.AutocompleteInteractionCreate) {
		called.Store(true)
	})

	if len(r.autocomplete) != 1 {
		t.Fatalf("expected 1 autocomplete handler, got %d", len(r.autocomplete))
	}

	handler, ok := r.autocomplete["ping"]
	if !ok {
		t.Fatal("autocomplete handler not found for key 'ping'")
	}
	handler(nil)
	if !called.Load() {
		t.Error("autocomplete handler was not called")
	}
}

func TestCommandRouter_RegisterComponent(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterComponent("btn_confirm", func(e *events.ComponentInteractionCreate) {
		called.Store(true)
	})

	if len(r.components) != 1 {
		t.Fatalf("expected 1 component handler, got %d", len(r.components))
	}

	handler, ok := r.components["btn_confirm"]
	if !ok {
		t.Fatal("component handler not found")
	}
	handler(nil)
	if !called.Load() {
		t.Error("component handler was not called")
	}
}

func TestCommandRouter_RegisterComponentPrefix(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterComponentPrefix("entity_remove:", func(e *events.ComponentInteractionCreate) {
		called.Store(true)
	})

	if len(r.componentPrefix) != 1 {
		t.Fatalf("expected 1 component prefix handler, got %d", len(r.componentPrefix))
	}

	handler, ok := r.componentPrefix["entity_remove:"]
	if !ok {
		t.Fatal("component prefix handler not found")
	}
	handler(nil)
	if !called.Load() {
		t.Error("component prefix handler was not called")
	}
}

func TestCommandRouter_RegisterModal(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterModal("edit_npc_modal", func(e *events.ModalSubmitInteractionCreate) {
		called.Store(true)
	})

	if len(r.modals) != 1 {
		t.Fatalf("expected 1 modal handler, got %d", len(r.modals))
	}

	handler, ok := r.modals["edit_npc_modal"]
	if !ok {
		t.Fatal("modal handler not found")
	}
	handler(nil)
	if !called.Load() {
		t.Error("modal handler was not called")
	}
}

func TestCommandRouter_HandleCommand_Found(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	cmd := discord.SlashCommandCreate{Name: "ping", Description: "pong"}
	r.RegisterCommand("ping", cmd, func(e *events.ApplicationCommandInteractionCreate) {
		called.Store(true)
	})

	e := makeSlashCommandEvent(t, `{"id": "1", "name": "ping", "type": 1}`)
	r.HandleCommand(e)

	if !called.Load() {
		t.Error("expected command handler to be called")
	}
}

func TestCommandRouter_HandleCommand_NotFound(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	// Register nothing, so "unknown" won't be found.
	e := makeSlashCommandEvent(t, `{"id": "1", "name": "unknown", "type": 1}`)
	// Should not panic; should call RespondEphemeral which uses the noopResponder.
	r.HandleCommand(e)
}

func TestCommandRouter_HandleCommand_Subcommand(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	cmd := discord.SlashCommandCreate{Name: "npc", Description: "npc commands"}
	r.RegisterCommand("npc/mute", cmd, func(e *events.ApplicationCommandInteractionCreate) {
		called.Store(true)
	})

	// Build a slash command with a subcommand option.
	e := makeSlashCommandEvent(t, `{
		"id": "1",
		"name": "npc",
		"type": 1,
		"options": [{"name": "mute", "type": 1}]
	}`)
	r.HandleCommand(e)

	if !called.Load() {
		t.Error("expected subcommand handler 'npc/mute' to be called")
	}
}

func TestCommandRouter_HandleAutocomplete_Found(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterAutocomplete("search", func(e *events.AutocompleteInteractionCreate) {
		called.Store(true)
	})

	e := makeAutocompleteEvent(t, `{
		"id": "1",
		"name": "search",
		"type": 1,
		"options": [{"name": "query", "type": 3, "value": "test", "focused": true}]
	}`)
	r.HandleAutocomplete(e)

	if !called.Load() {
		t.Error("expected autocomplete handler to be called")
	}
}

func TestCommandRouter_HandleAutocomplete_NotFound(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	e := makeAutocompleteEvent(t, `{
		"id": "1",
		"name": "nosuch",
		"type": 1,
		"options": [{"name": "q", "type": 3, "value": "", "focused": true}]
	}`)
	// Should not panic; calls e.AutocompleteResult(nil) which uses noopResponder.
	r.HandleAutocomplete(e)
}

func TestCommandRouter_HandleAutocomplete_Subcommand(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterAutocomplete("npc/search", func(e *events.AutocompleteInteractionCreate) {
		called.Store(true)
	})

	e := makeAutocompleteEvent(t, `{
		"id": "1",
		"name": "npc",
		"type": 1,
		"options": [{"name": "search", "type": 1, "options": [{"name": "query", "type": 3, "value": "test", "focused": true}]}]
	}`)
	r.HandleAutocomplete(e)

	if !called.Load() {
		t.Error("expected subcommand autocomplete handler 'npc/search' to be called")
	}
}

func TestCommandRouter_HandleComponent_ExactMatch(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterComponent("btn_ok", func(e *events.ComponentInteractionCreate) {
		called.Store(true)
	})

	e := makeComponentEvent(t, "btn_ok")
	r.HandleComponent(e)

	if !called.Load() {
		t.Error("expected component handler to be called")
	}
}

func TestCommandRouter_HandleComponent_PrefixMatch(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterComponentPrefix("entity_remove:", func(e *events.ComponentInteractionCreate) {
		called.Store(true)
	})

	e := makeComponentEvent(t, "entity_remove:abc-123")
	r.HandleComponent(e)

	if !called.Load() {
		t.Error("expected component prefix handler to be called")
	}
}

func TestCommandRouter_HandleComponent_ExactBeforePrefix(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var exactCalled, prefixCalled atomic.Bool
	r.RegisterComponent("entity_remove:special", func(e *events.ComponentInteractionCreate) {
		exactCalled.Store(true)
	})
	r.RegisterComponentPrefix("entity_remove:", func(e *events.ComponentInteractionCreate) {
		prefixCalled.Store(true)
	})

	e := makeComponentEvent(t, "entity_remove:special")
	r.HandleComponent(e)

	if !exactCalled.Load() {
		t.Error("expected exact match handler to be called")
	}
	if prefixCalled.Load() {
		t.Error("did not expect prefix handler to be called when exact match exists")
	}
}

func TestCommandRouter_HandleComponent_NotFound(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	e := makeComponentEvent(t, "unknown_button")
	// Should not panic; calls RespondEphemeral with noopResponder.
	r.HandleComponent(e)
}

func TestCommandRouter_HandleModal_Found(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	var called atomic.Bool
	r.RegisterModal("edit_npc", func(e *events.ModalSubmitInteractionCreate) {
		called.Store(true)
	})

	e := makeModalEvent(t, "edit_npc")
	r.HandleModal(e)

	if !called.Load() {
		t.Error("expected modal handler to be called")
	}
}

func TestCommandRouter_HandleModal_NotFound(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	e := makeModalEvent(t, "unknown_modal")
	// Should not panic; calls RespondEphemeral with noopResponder.
	r.HandleModal(e)
}

func TestCommandKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dataJSON string
		want     string
	}{
		{
			name:     "top-level command",
			dataJSON: `{"id": "1", "name": "ping", "type": 1}`,
			want:     "ping",
		},
		{
			name:     "subcommand",
			dataJSON: `{"id": "1", "name": "npc", "type": 1, "options": [{"name": "mute", "type": 1}]}`,
			want:     "npc/mute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var data discord.SlashCommandInteractionData
			if err := json.Unmarshal([]byte(tt.dataJSON), &data); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			got := commandKey(data)
			if got != tt.want {
				t.Errorf("commandKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutocompleteKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data discord.AutocompleteInteractionData
		want string
	}{
		{
			name: "top-level",
			data: discord.AutocompleteInteractionData{CommandName: "search"},
			want: "search",
		},
		{
			name: "subcommand",
			data: func() discord.AutocompleteInteractionData {
				sub := "list"
				return discord.AutocompleteInteractionData{
					CommandName:    "npc",
					SubCommandName: &sub,
				}
			}(),
			want: "npc/list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := autocompleteKey(tt.data)
			if got != tt.want {
				t.Errorf("autocompleteKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommandRouter_ApplicationCommands_HandlerOnlyExcluded(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	// RegisterHandler (no command def) should not appear in ApplicationCommands.
	r.RegisterHandler("hidden", func(e *events.ApplicationCommandInteractionCreate) {})

	// RegisterCommand should appear.
	cmd := discord.SlashCommandCreate{Name: "visible", Description: "shows up"}
	r.RegisterCommand("visible", cmd, func(e *events.ApplicationCommandInteractionCreate) {})

	cmds := r.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command (only the one with definition), got %d", len(cmds))
	}
	if cmds[0].CommandName() != "visible" {
		t.Errorf("expected 'visible', got %q", cmds[0].CommandName())
	}
}

func TestCommandRouter_ApplicationCommands_Empty(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()
	cmds := r.ApplicationCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands for empty router, got %d", len(cmds))
	}
}
