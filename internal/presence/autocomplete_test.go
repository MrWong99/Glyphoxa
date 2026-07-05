package presence

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

func choice(name string) discord.AutocompleteChoice {
	return discord.AutocompleteChoiceString{Name: name, Value: name}
}

func TestAutocompleteChoicesGating(t *testing.T) {
	reg := testRegistry(testGuild, operatorID)
	reg.Register(Command{Path: "glyphoxa search", GMOnly: true, Autocomplete: func(context.Context, *Autocomplete) ([]discord.AutocompleteChoice, error) {
		return []discord.AutocompleteChoice{choice("hit")}, nil
	}})
	reg.Register(Command{Path: "roll"}) // no autocomplete handler
	reg.Register(Command{Path: "find", Autocomplete: func(context.Context, *Autocomplete) ([]discord.AutocompleteChoice, error) {
		return nil, errors.New("boom")
	}})

	op := &Autocomplete{guildID: testGuild, userID: operatorID}
	stranger := &Autocomplete{guildID: testGuild, userID: strangerID}
	ctx := context.Background()

	if got := reg.autocompleteChoices(ctx, "nope", op); got == nil || len(got) != 0 {
		t.Errorf("unknown command choices = %v, want empty non-nil", got)
	}
	if got := reg.autocompleteChoices(ctx, "roll", op); len(got) != 0 {
		t.Errorf("no-autocomplete command choices = %v, want empty", got)
	}
	if got := reg.autocompleteChoices(ctx, "glyphoxa search", op); len(got) != 1 {
		t.Errorf("authorized operator choices = %v, want 1", got)
	}
	// GM-only command must NOT leak choices (or even their existence) to a
	// non-operator: an empty, non-nil slice.
	got := reg.autocompleteChoices(ctx, "glyphoxa search", stranger)
	if got == nil || len(got) != 0 {
		t.Errorf("non-operator GM-only choices = %v, want empty non-nil (no leak)", got)
	}
	if got := reg.autocompleteChoices(ctx, "find", stranger); len(got) != 0 {
		t.Errorf("handler-error choices = %v, want empty", got)
	}
}

func TestAutocompleteFocused(t *testing.T) {
	ac := &Autocomplete{
		guildID: testGuild,
		userID:  operatorID,
		data: discord.AutocompleteInteractionData{
			CommandName: "find",
			Options: map[string]discord.AutocompleteOption{
				"query": {
					Name:    "query",
					Type:    discord.ApplicationCommandOptionTypeString,
					Value:   json.RawMessage(`"lich"`),
					Focused: true,
				},
			},
		},
	}
	name, value := ac.Focused()
	if name != "query" || value != "lich" {
		t.Errorf("Focused = (%q, %q), want (query, lich)", name, value)
	}
	if ac.UserID() != operatorID || ac.GuildID() != testGuild {
		t.Errorf("identity = (%q, %q), want (%q, %q)", ac.UserID(), ac.GuildID(), operatorID, testGuild)
	}

	// No focused option (autocomplete fired before typing) must not panic.
	empty := &Autocomplete{data: discord.AutocompleteInteractionData{}}
	if n, v := empty.Focused(); n != "" || v != "" {
		t.Errorf("empty Focused = (%q, %q), want empty", n, v)
	}
}

func TestHandleAutocompleteEndToEnd(t *testing.T) {
	reg := testRegistry(testGuild, operatorID)
	reg.Register(Command{Path: "find", Autocomplete: func(_ context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
		_, v := ac.Focused()
		return []discord.AutocompleteChoice{choice(v + "-match")}, nil
	}})

	payload := `{
		"id": "1", "application_id": "2", "type": 4, "token": "t", "version": 1,
		"guild_id": "` + testGuild + `",
		"member": {"user": {"id": "` + operatorID + `", "username": "gm"}},
		"data": {"id": "3", "name": "find", "options": [{"type": 3, "name": "query", "value": "li", "focused": true}]}
	}`
	var ai discord.AutocompleteInteraction
	if err := json.Unmarshal([]byte(payload), &ai); err != nil {
		t.Fatalf("unmarshal autocomplete: %v", err)
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

	if len(got.Choices) != 1 || got.Choices[0].ChoiceName() != "li-match" {
		t.Errorf("autocomplete result = %+v, want one 'li-match' choice", got.Choices)
	}
}
