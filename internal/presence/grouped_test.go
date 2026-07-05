package presence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

// TestHandleCommandEndToEndGroupedSubcommand pins issue #7 for commands: a real
// grouped `/glyphoxa <sub>` interaction — disgo populates SubCommandName — routes
// through dispatchKey to the "glyphoxa ping" handler with its option. #108
// consumes this exact path first.
func TestHandleCommandEndToEndGroupedSubcommand(t *testing.T) {
	reg := testRegistry(testGuild, operatorID) // operator is allowlisted for the GM-only command
	var gotMsg string
	ran := false
	reg.Register(Command{Path: "glyphoxa ping", GMOnly: true, Handle: func(_ context.Context, ic *Interaction) error {
		ran = true
		gotMsg, _ = ic.String("msg")
		return ic.Reply("pong: " + gotMsg)
	}})

	payload := `{
		"id": "1", "application_id": "2", "type": 2, "token": "t", "version": 1,
		"guild_id": "` + testGuild + `",
		"member": {"user": {"id": "` + operatorID + `", "username": "gm"}},
		"data": {"id": "3", "name": "glyphoxa", "type": 1,
			"options": [{"type": 1, "name": "ping", "options": [{"type": 3, "name": "msg", "value": "hi"}]}]}
	}`
	var aci discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(payload), &aci); err != nil {
		t.Fatalf("unmarshal grouped interaction: %v", err)
	}

	var reply discord.MessageCreate
	e := &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: aci,
		Respond: func(_ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			reply = data.(discord.MessageCreate)
			return nil
		},
	}
	reg.HandleCommand(e)

	if !ran {
		t.Fatal("grouped subcommand handler did not run (dispatchKey missed SubCommandName?)")
	}
	if gotMsg != "hi" {
		t.Errorf("subcommand option msg = %q, want hi", gotMsg)
	}
	if reply.Content != "pong: hi" {
		t.Errorf("reply = %q, want 'pong: hi'", reply.Content)
	}
}

// TestHandleAutocompleteEndToEndGroupedSubcommand pins issue #7 for autocomplete:
// a real grouped `/glyphoxa search` autocomplete routes to the "glyphoxa search"
// handler for an authorized operator.
func TestHandleAutocompleteEndToEndGroupedSubcommand(t *testing.T) {
	reg := testRegistry(testGuild, operatorID)
	reg.Register(Command{Path: "glyphoxa search", GMOnly: true, Autocomplete: func(_ context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
		_, v := ac.Focused()
		return []discord.AutocompleteChoice{choice(v + "-hit")}, nil
	}})

	payload := `{
		"id": "1", "application_id": "2", "type": 4, "token": "t", "version": 1,
		"guild_id": "` + testGuild + `",
		"member": {"user": {"id": "` + operatorID + `", "username": "gm"}},
		"data": {"id": "3", "name": "glyphoxa",
			"options": [{"type": 1, "name": "search", "options": [{"type": 3, "name": "query", "value": "li", "focused": true}]}]}
	}`
	var ai discord.AutocompleteInteraction
	if err := json.Unmarshal([]byte(payload), &ai); err != nil {
		t.Fatalf("unmarshal grouped autocomplete: %v", err)
	}

	var got discord.AutocompleteResult
	e := &events.AutocompleteInteractionCreate{
		AutocompleteInteraction: ai,
		Respond: func(_ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			got = data.(discord.AutocompleteResult)
			return nil
		},
	}
	reg.HandleAutocomplete(e)

	if len(got.Choices) != 1 || got.Choices[0].ChoiceName() != "li-hit" {
		t.Errorf("grouped autocomplete result = %+v, want one 'li-hit'", got.Choices)
	}
}
