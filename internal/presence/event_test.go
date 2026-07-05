package presence

import (
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// rollInteractionJSON is a real Discord slash-command interaction payload for
// `/roll dice:<expr>` in the configured Guild, invoked by userID. Building the
// event from JSON exercises the actual disgo unwrap path (option extraction,
// Guild/user resolution) that HandleCommand relies on — not just the fake seam.
func rollInteractionJSON(guildID, userID, expr string) string {
	return `{
		"id": "100000000000000001",
		"application_id": "200000000000000002",
		"type": 2,
		"token": "interaction-token",
		"version": 1,
		"guild_id": "` + guildID + `",
		"member": {"user": {"id": "` + userID + `", "username": "gm"}},
		"data": {
			"id": "300000000000000003",
			"name": "roll",
			"type": 1,
			"options": [{"name": "dice", "type": 3, "value": "` + expr + `"}]
		}
	}`
}

func TestHandleCommandEndToEndRoll(t *testing.T) {
	// Seeded dice + an identically-seeded oracle so the reply is checkable.
	const s1, s2 = 0x243f6a8885a308d3, 0x13198a2e03707344
	dice := tool.NewDiceWithRand(rand.New(rand.NewPCG(s1, s2)))
	oracle := tool.NewDiceWithRand(rand.New(rand.NewPCG(s1, s2)))

	reg := testRegistry(testGuild, "")
	reg.Register(RollCommand(dice))

	var aci discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(rollInteractionJSON(testGuild, operatorID, "2d6")), &aci); err != nil {
		t.Fatalf("unmarshal interaction: %v", err)
	}

	var gotType discord.InteractionResponseType
	var gotMsg discord.MessageCreate
	e := &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: aci,
		Respond: func(rt discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			gotType = rt
			gotMsg = data.(discord.MessageCreate)
			return nil
		},
	}

	reg.HandleCommand(e)

	if gotType != discord.InteractionResponseTypeCreateMessage {
		t.Errorf("response type = %v, want CreateMessage", gotType)
	}
	if gotMsg.Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("a valid roll replied ephemerally; a result is public")
	}
	want := oracleRoll(t, oracle, 2, 6)
	if gotMsg.Content != want {
		t.Errorf("reply = %q, want the Dice Tool's exact string %q", gotMsg.Content, want)
	}
}
