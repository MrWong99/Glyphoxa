package presence

import (
	"context"
	"testing"

	"github.com/disgoorg/disgo/bot"
)

func TestInteractionAccessors(t *testing.T) {
	resp := &fakeResponder{}
	ic := &Interaction{
		guildID: testGuild,
		userID:  operatorID,
		opts:    fakeOpts{s: map[string]string{"name": "bart"}, i: map[string]int{"n": 3}},
		resp:    resp,
	}

	if ic.GuildID() != testGuild {
		t.Errorf("GuildID = %q, want %q", ic.GuildID(), testGuild)
	}
	if ic.UserID() != operatorID {
		t.Errorf("UserID = %q, want %q", ic.UserID(), operatorID)
	}
	if v, ok := ic.String("name"); !ok || v != "bart" {
		t.Errorf("String(name) = (%q, %v), want (bart, true)", v, ok)
	}
	if _, ok := ic.String("missing"); ok {
		t.Error("String(missing) ok = true, want false")
	}
	if v, ok := ic.Int("n"); !ok || v != 3 {
		t.Errorf("Int(n) = (%d, %v), want (3, true)", v, ok)
	}
	if _, ok := ic.Int("missing"); ok {
		t.Error("Int(missing) ok = true, want false")
	}

	// Defer + Followup route through the responder (the slow-handler seam
	// #108/#120 use).
	if err := ic.Defer(true); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if resp.deferred == nil || !*resp.deferred {
		t.Errorf("Defer did not record an ephemeral defer: %v", resp.deferred)
	}
	if err := ic.Followup("later", true); err != nil {
		t.Fatalf("Followup: %v", err)
	}
	if len(resp.followups) != 1 || resp.followups[0].content != "later" || !resp.followups[0].ephemeral {
		t.Errorf("Followup = %+v, want one ephemeral 'later'", resp.followups)
	}
}

func TestInteractionNilOptions(t *testing.T) {
	ic := &Interaction{resp: &fakeResponder{}}
	if _, ok := ic.String("x"); ok {
		t.Error("String on nil options ok = true, want false")
	}
	if _, ok := ic.Int("x"); ok {
		t.Error("Int on nil options ok = true, want false")
	}
}

func TestRestRegisterInvalidGuild(t *testing.T) {
	// snowflake.Parse rejects a non-numeric Guild id before any REST call, so a
	// nil-Rest client is safe here.
	if err := restRegister(context.Background(), &bot.Client{}, "not-a-snowflake", nil); err == nil {
		t.Fatal("restRegister with an invalid guild id = nil, want a parse error")
	}
}
