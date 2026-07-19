package presence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// replyKind names the responder method a recorded message came through, so a test can
// assert HOW a message was delivered (a placeholder edit vs a fresh followup) directly,
// not only via its visibility side-effect — a dangling "thinking…" placeholder is then
// observable as "the first post-Defer message was a followup, not an edit" (#335).
type replyKind string

const (
	kindReply    replyKind = "reply"    // a pre-Defer CreateMessage
	kindEdit     replyKind = "edit"     // an EditOriginal of the deferred placeholder
	kindFollowup replyKind = "followup" // a fresh CreateFollowupMessage
)

// recordedReply captures one responder call for assertions.
type recordedReply struct {
	content   string
	ephemeral bool
	kind      replyKind
}

// fakeResponder records reply/defer/followup calls instead of hitting Discord. It
// models Discord's CURRENT interaction-response behavior so tests catch a visibility
// regression. Discord DEPRECATED the first-followup-edits shim (#335): a followup
// after a Defer no longer implicitly edits the "thinking…" placeholder — it ALWAYS
// creates a fresh message honoring its own ephemeral flag, leaving the placeholder
// dangling. The ONLY way to resolve the deferred placeholder is now editOriginal,
// which edits it in place at the Defer's fixed visibility (the ephemeral flag is
// ignored on the original-response edit). The dispatch layer therefore routes the
// FIRST post-Defer reply through editOriginal and later ones through followup. Every
// post-defer message is recorded in followups in order, each with its EFFECTIVE
// visibility, so a test asserts both the placeholder edit and the real followups.
type fakeResponder struct {
	replies   []recordedReply
	followups []recordedReply
	deferred  *bool
	// editErrs is a queue of errors returned by successive editOriginal calls (a nil
	// entry, or an exhausted queue, is a success). It models a Discord 5xx on the
	// original-response edit so the retry-on-failed-edit path (#335) has coverage: a
	// failed edit records nothing and must NOT consume the placeholder.
	editErrs []error
}

func (f *fakeResponder) reply(content string, ephemeral bool) error {
	f.replies = append(f.replies, recordedReply{content, ephemeral, kindReply})
	return nil
}

func (f *fakeResponder) deferResponse(ephemeral bool) error {
	f.deferred = &ephemeral
	return nil
}

func (f *fakeResponder) followup(content string, ephemeral bool) error {
	// Post-deprecation: a followup is always a fresh message honoring its own flag; it
	// does NOT edit the placeholder.
	f.followups = append(f.followups, recordedReply{content, ephemeral, kindFollowup})
	return nil
}

func (f *fakeResponder) editOriginal(content string) error {
	if len(f.editErrs) > 0 {
		err := f.editErrs[0]
		f.editErrs = f.editErrs[1:]
		if err != nil {
			// A failed edit records nothing: the placeholder is still unresolved.
			return err
		}
	}
	// Editing the original response keeps the Defer's visibility regardless of any flag.
	vis := true
	if f.deferred != nil {
		vis = *f.deferred
	}
	f.followups = append(f.followups, recordedReply{content, vis, kindEdit})
	return nil
}

// fakeOpts is a map-backed optionSource for dispatch tests.
type fakeOpts struct {
	s map[string]string
	i map[string]int
}

func (f fakeOpts) OptString(name string) (string, bool) { v, ok := f.s[name]; return v, ok }
func (f fakeOpts) OptInt(name string) (int, bool)       { v, ok := f.i[name]; return v, ok }

// testRegistry builds a Registry whose Gate maps `guild` to tenantA and grants
// `gmID` GM standing in tenantA. An unmapped Guild (e.g. otherGuild) resolves to no
// Tenant → ErrWrongGuild.
func testRegistry(guild string, gmID string) *Registry {
	return NewRegistry(NewGate(gmIn(tenantA, gmID), fakeTenants{guild: tenantA}), nil)
}

// TestDispatchPopulatesTenantID pins #490 test (5): dispatch resolves the
// interaction's Guild to its owning Tenant via the Gate and threads it onto the
// Interaction, so every tenant-scoped handler reads ic.TenantID().
func TestDispatchPopulatesTenantID(t *testing.T) {
	reg := testRegistry(testGuild, operatorID)
	var got = tenantB // seed with the wrong value so a no-op is caught
	reg.Register(Command{Path: "grab", GMOnly: true, Handle: func(_ context.Context, ic *Interaction) error {
		got = ic.TenantID()
		return ic.Reply("ok")
	}})

	reg.dispatch(context.Background(), "grab", &Interaction{guildID: testGuild, userID: operatorID, resp: &fakeResponder{}})

	if got != tenantA {
		t.Errorf("handler saw tenant %s, want the Guild's resolved tenant %s", got, tenantA)
	}
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

// TestDispatchFirstPostDeferReplyEditsOriginal pins issue #335: Discord deprecated the
// first-followup-edits shim, so the dispatch layer must route the FIRST post-Defer
// reply through EditOriginal (consuming the "thinking…" placeholder at the Defer's
// fixed visibility) and only LATER replies through CreateFollowupMessage (fresh
// messages honoring their own flag). It is a registry-wide routing rule, not a
// per-command one: a plain handler that Defers ephemerally and then Replies PUBLICLY
// twice must land its first reply as an ephemeral placeholder edit and its second as a
// real public followup.
func TestDispatchFirstPostDeferReplyEditsOriginal(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "multi", Handle: func(_ context.Context, ic *Interaction) error {
		if err := ic.Defer(true); err != nil { // ephemeral placeholder
			return err
		}
		if err := ic.Reply("first"); err != nil { // public content
			return err
		}
		return ic.Reply("second") // public content
	}})

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "multi", &Interaction{guildID: testGuild, userID: strangerID, resp: resp})

	if len(resp.replies) != 0 {
		t.Fatalf("post-Defer must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 2 {
		t.Fatalf("want 2 post-Defer messages (a placeholder edit + a followup), got %+v", resp.followups)
	}
	// The FIRST reply consumes the placeholder via EditOriginal: delivered as an edit
	// (no dangling placeholder), visibility forced to the Defer's (ephemeral), NOT the
	// reply's public flag.
	if resp.followups[0].content != "first" || !resp.followups[0].ephemeral || resp.followups[0].kind != kindEdit {
		t.Errorf("first post-Defer message = %+v, want a kindEdit of \"first\" at the Defer's ephemeral visibility", resp.followups[0])
	}
	// The SECOND reply is a real followup honoring its own public flag.
	if resp.followups[1].content != "second" || resp.followups[1].ephemeral || resp.followups[1].kind != kindFollowup {
		t.Errorf("second post-Defer message = %+v, want a public kindFollowup of \"second\"", resp.followups[1])
	}
}

// TestDispatchFailedEditOriginalRetriesOnNextReply pins the retry-on-failed-edit
// contract (#335): the placeholder is marked consumed ONLY after EditOriginal
// succeeds. When Discord 5xxs the first edit, the handler's error propagates and the
// dispatch generic-error ReplyEphemeral must edit AGAIN (a second kindEdit), not fall
// through to a followup that would strand the "thinking…" placeholder forever. A
// mark-before-edit regression would route the retry to a followup and fail this.
func TestDispatchFailedEditOriginalRetriesOnNextReply(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "flaky", Handle: func(_ context.Context, ic *Interaction) error {
		if err := ic.Defer(true); err != nil {
			return err
		}
		return ic.Reply("body") // first edit attempt — Discord 5xxs it
	}})

	// One queued edit error: the first editOriginal fails, the retry succeeds.
	resp := &fakeResponder{editErrs: []error{errors.New("discord 500")}}
	reg.dispatch(context.Background(), "flaky", &Interaction{guildID: testGuild, userID: strangerID, resp: resp})

	if len(resp.replies) != 0 {
		t.Fatalf("post-Defer must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 {
		t.Fatalf("want exactly one recorded message (the successful retry edit), got %+v", resp.followups)
	}
	got := resp.followups[0]
	if got.kind != kindEdit {
		t.Errorf("retry after a failed edit = %s, want kindEdit (placeholder consumed on retry, not stranded via followup)", got.kind)
	}
	if !got.ephemeral {
		t.Errorf("retry edit visibility = public, want the Defer's ephemeral")
	}
	if !strings.Contains(strings.ToLower(got.content), "went wrong") {
		t.Errorf("retry content = %q, want the generic dispatch error reply", got.content)
	}
}

func keys(m map[string]discord.SlashCommandCreate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSetActiveGatesDispatch covers sequence (4): an inactive Registry (a
// non-elected presence owner, #492) drops a slash-command interaction entirely —
// the handler never runs and NOTHING is replied — while an active one dispatches
// normally. This is the mechanism that makes N Voice Instances on one shared
// central token safe: every session receives every INTERACTION_CREATE (P5), and
// only the elected owner acts on it.
func TestSetActiveGatesDispatch(t *testing.T) {
	reg := testRegistry(testGuild, "")
	ran := false
	reg.Register(Command{Path: "ping", Handle: func(_ context.Context, ic *Interaction) error {
		ran = true
		return ic.Reply("pong")
	}})

	reg.SetActive(false)
	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "ping", &Interaction{guildID: testGuild, userID: strangerID, resp: resp})
	if ran {
		t.Error("inactive Registry must not run the handler")
	}
	if len(resp.replies) != 0 {
		t.Errorf("inactive Registry must not reply, got %+v", resp.replies)
	}

	reg.SetActive(true)
	resp2 := &fakeResponder{}
	reg.dispatch(context.Background(), "ping", &Interaction{guildID: testGuild, userID: strangerID, resp: resp2})
	if !ran {
		t.Error("active Registry must run the handler")
	}
	if len(resp2.replies) != 1 || resp2.replies[0].content != "pong" {
		t.Errorf("active Registry should reply once with pong, got %+v", resp2.replies)
	}
}

// TestSetActiveGatesAutocomplete covers sequence (4) for autocomplete: an inactive
// Registry returns no choices without running the autocomplete handler.
func TestSetActiveGatesAutocomplete(t *testing.T) {
	reg := testRegistry(testGuild, "")
	ran := false
	reg.Register(Command{Path: "pick", Handle: func(context.Context, *Interaction) error { return nil },
		Autocomplete: func(context.Context, *Autocomplete) ([]discord.AutocompleteChoice, error) {
			ran = true
			return []discord.AutocompleteChoice{discord.AutocompleteChoiceString{Name: "x", Value: "x"}}, nil
		}})

	reg.SetActive(false)
	choices := reg.autocompleteChoices(context.Background(), "pick", &Autocomplete{guildID: testGuild, userID: strangerID})
	if ran {
		t.Error("inactive Registry must not run the autocomplete handler")
	}
	if len(choices) != 0 {
		t.Errorf("inactive Registry must return no choices, got %d", len(choices))
	}
}

// TestSetActiveGatesComponent covers sequence (4) for message components: an
// inactive Registry fans no component interaction out to its handlers.
func TestSetActiveGatesComponent(t *testing.T) {
	reg := testRegistry(testGuild, "")
	ran := false
	reg.RegisterComponentHandler(func(*events.ComponentInteractionCreate) { ran = true })

	reg.SetActive(false)
	reg.HandleComponent(nil)
	if ran {
		t.Error("inactive Registry must not invoke component handlers")
	}

	reg.SetActive(true)
	reg.HandleComponent(nil)
	if !ran {
		t.Error("active Registry must invoke component handlers")
	}
}

// TestExactlyOnceAcrossTwoRegistries covers sequence (5): the exactly-once
// guarantee behind ADR-0057 (c). Two Voice Instances' command Registries receive
// the SAME interaction (Discord delivers it to every session on the shared token);
// only the elected owner is active, so the handler runs exactly once total and the
// non-owner replies nothing.
func TestExactlyOnceAcrossTwoRegistries(t *testing.T) {
	owner := testRegistry(testGuild, "")
	nonOwner := testRegistry(testGuild, "")
	dispatches := 0
	handler := func(_ context.Context, ic *Interaction) error {
		dispatches++
		return ic.Reply("done")
	}
	owner.Register(Command{Path: "roll", Handle: handler})
	nonOwner.Register(Command{Path: "roll", Handle: handler})

	owner.SetActive(true)
	nonOwner.SetActive(false)

	ownerResp := &fakeResponder{}
	nonOwnerResp := &fakeResponder{}
	owner.dispatch(context.Background(), "roll", &Interaction{guildID: testGuild, userID: strangerID, resp: ownerResp})
	nonOwner.dispatch(context.Background(), "roll", &Interaction{guildID: testGuild, userID: strangerID, resp: nonOwnerResp})

	if dispatches != 1 {
		t.Fatalf("handler ran %d times, want exactly once across the fleet", dispatches)
	}
	if len(ownerResp.replies) != 1 {
		t.Errorf("owner should reply once, got %+v", ownerResp.replies)
	}
	if len(nonOwnerResp.replies) != 0 {
		t.Errorf("non-owner must reply nothing, got %+v", nonOwnerResp.replies)
	}
}
