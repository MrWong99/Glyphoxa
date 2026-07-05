package presence

import (
	"context"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// recordedReply captures one responder call for assertions.
type recordedReply struct {
	content   string
	ephemeral bool
}

// fakeResponder records reply/defer/followup calls instead of hitting Discord.
type fakeResponder struct {
	replies   []recordedReply
	followups []recordedReply
	deferred  *bool
}

func (f *fakeResponder) reply(content string, ephemeral bool) error {
	f.replies = append(f.replies, recordedReply{content, ephemeral})
	return nil
}

func (f *fakeResponder) deferResponse(ephemeral bool) error {
	f.deferred = &ephemeral
	return nil
}

func (f *fakeResponder) followup(content string, ephemeral bool) error {
	f.followups = append(f.followups, recordedReply{content, ephemeral})
	return nil
}

// fakeOpts is a map-backed optionSource for dispatch tests.
type fakeOpts struct {
	s map[string]string
	i map[string]int
}

func (f fakeOpts) OptString(name string) (string, bool) { v, ok := f.s[name]; return v, ok }
func (f fakeOpts) OptInt(name string) (int, bool)       { v, ok := f.i[name]; return v, ok }

func testRegistry(guild string, allow string) *Registry {
	return NewRegistry(NewGate(auth.ParseOperatorAllowlist(allow), fixedGuild(guild)), nil)
}

func TestDefinitionsMergeFlatAndGroup(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "roll", Description: "Roll dice"})
	reg.Register(Command{
		Path:        "glyphoxa x",
		Description: "Do x",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{Name: "q", Description: "query", Required: true},
		},
	})

	defs := reg.Definitions()
	if len(defs) != 2 {
		t.Fatalf("Definitions len = %d, want 2 (one flat + one merged group)", len(defs))
	}

	byName := map[string]discord.SlashCommandCreate{}
	for _, d := range defs {
		sc, ok := d.(discord.SlashCommandCreate)
		if !ok {
			t.Fatalf("definition %T is not a SlashCommandCreate", d)
		}
		byName[sc.Name] = sc
	}

	if _, ok := byName["roll"]; !ok {
		t.Errorf("missing flat /roll command; have %v", keys(byName))
	}
	g, ok := byName["glyphoxa"]
	if !ok {
		t.Fatalf("missing merged /glyphoxa command; have %v", keys(byName))
	}
	if len(g.Options) != 1 {
		t.Fatalf("/glyphoxa options = %d, want 1 subcommand", len(g.Options))
	}
	sub, ok := g.Options[0].(discord.ApplicationCommandOptionSubCommand)
	if !ok {
		t.Fatalf("/glyphoxa option 0 is %T, want SubCommand", g.Options[0])
	}
	if sub.Name != "x" {
		t.Errorf("subcommand name = %q, want x", sub.Name)
	}
	if len(sub.Options) != 1 || sub.Options[0].(discord.ApplicationCommandOptionString).Name != "q" {
		t.Errorf("subcommand did not carry its own option q: %+v", sub.Options)
	}
}

func TestDefinitionsMergesMultipleSubcommands(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(
		Command{Path: "glyphoxa start", Description: "start"},
		Command{Path: "glyphoxa end", Description: "end"},
	)
	defs := reg.Definitions()
	if len(defs) != 1 {
		t.Fatalf("Definitions len = %d, want 1 merged /glyphoxa", len(defs))
	}
	g := defs[0].(discord.SlashCommandCreate)
	if len(g.Options) != 2 {
		t.Errorf("/glyphoxa subcommands = %d, want 2", len(g.Options))
	}
}

func TestDispatchRoutesWithParsedOption(t *testing.T) {
	reg := testRegistry(testGuild, "")
	var gotArg string
	ran := false
	reg.Register(Command{Path: "echo", Handle: func(_ context.Context, ic *Interaction) error {
		ran = true
		gotArg, _ = ic.String("msg")
		return ic.Reply("said: " + gotArg)
	}})

	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: strangerID, opts: fakeOpts{s: map[string]string{"msg": "hi"}}, resp: resp}
	reg.dispatch(context.Background(), "echo", ic)

	if !ran {
		t.Fatal("handler did not run")
	}
	if gotArg != "hi" {
		t.Errorf("parsed option = %q, want hi", gotArg)
	}
	if len(resp.replies) != 1 || resp.replies[0].content != "said: hi" || resp.replies[0].ephemeral {
		t.Errorf("reply = %+v, want one public 'said: hi'", resp.replies)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	reg := testRegistry(testGuild, "")
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: strangerID, resp: resp}

	// Must not panic and must answer ephemerally.
	reg.dispatch(context.Background(), "nope", ic)

	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("unknown command reply = %+v, want one ephemeral", resp.replies)
	}
}

func TestDispatchGateDenials(t *testing.T) {
	reg := testRegistry(testGuild, operatorID)
	reg.Register(Command{Path: "gm", GMOnly: true, Handle: func(context.Context, *Interaction) error {
		t.Fatal("GM handler ran despite denial")
		return nil
	}})
	reg.Register(Command{Path: "any", Handle: func(_ context.Context, ic *Interaction) error {
		return ic.Reply("ok")
	}})

	// Non-operator invoking a GM-only command → ErrNotOperator text.
	notOp := &fakeResponder{}
	reg.dispatch(context.Background(), "gm", &Interaction{guildID: testGuild, userID: strangerID, resp: notOp})
	// Wrong-Guild invoking an anyone command → ErrWrongGuild text.
	wrongGuild := &fakeResponder{}
	reg.dispatch(context.Background(), "any", &Interaction{guildID: otherGuild, userID: strangerID, resp: wrongGuild})

	if len(notOp.replies) != 1 || !notOp.replies[0].ephemeral {
		t.Fatalf("gm denial reply = %+v, want one ephemeral", notOp.replies)
	}
	if len(wrongGuild.replies) != 1 || !wrongGuild.replies[0].ephemeral {
		t.Fatalf("wrong-guild reply = %+v, want one ephemeral", wrongGuild.replies)
	}
	if notOp.replies[0].content == wrongGuild.replies[0].content {
		t.Errorf("ErrNotOperator and ErrWrongGuild map to the same text %q; want distinct", notOp.replies[0].content)
	}
}

func TestDispatchHandlerErrorRepliesGeneric(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "boom", Handle: func(context.Context, *Interaction) error {
		return context.DeadlineExceeded // stand-in for an unexpected failure
	}})

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "boom", &Interaction{guildID: testGuild, userID: strangerID, resp: resp})

	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("handler-error reply = %+v, want one ephemeral", resp.replies)
	}
	if !strings.Contains(strings.ToLower(resp.replies[0].content), "went wrong") {
		t.Errorf("handler-error reply = %q, want a generic failure message", resp.replies[0].content)
	}
}

func keys(m map[string]discord.SlashCommandCreate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
