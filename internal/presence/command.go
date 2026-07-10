package presence

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// interactionTimeout bounds a handler's work well under Discord's 3s
// initial-response deadline: past it Discord drops the interaction token and the
// reply fails. Every dispatched handler runs on a context deadlined to this.
const interactionTimeout = 2500 * time.Millisecond

// commandGroup is the single grouped-command prefix v1.0 ships (ADR-0010):
// admin/session commands register as `/glyphoxa <sub>`, merged into one
// SlashCommandCreate. High-frequency commands (e.g. /roll) stay flat.
const commandGroup = "glyphoxa"

// commandGroupDescription is the top-level description Discord requires for the
// merged /glyphoxa command (its subcommands carry their own descriptions).
const commandGroupDescription = "Glyphoxa game-master commands"

// Handler runs one slash-command interaction. It owns the user-facing reply:
// domain errors (a malformed argument) are reported with ic.ReplyEphemeral and
// the handler returns nil; a returned error is an UNEXPECTED failure, which the
// Registry logs and answers with a generic ephemeral message. Either way the
// interaction is never left silently un-answered.
type Handler func(ctx context.Context, ic *Interaction) error

// AutocompleteHandler produces choices for an in-progress option value. A nil
// AutocompleteHandler on a Command means the command has no autocomplete.
type AutocompleteHandler func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error)

// Command is one registered slash command. Path is either a flat name ("roll")
// or a grouped `"glyphoxa <sub>"` (ADR-0010) — the Registry merges every
// "glyphoxa *" Path into ONE /glyphoxa SlashCommandCreate whose subcommands are
// the individual Paths.
type Command struct {
	Path        string
	Description string
	Options     []discord.ApplicationCommandOption
	// GMOnly false = anyone in the configured Guild (Gate.CheckGuild only); true
	// = operator-allowlisted GM (Gate.CheckGM).
	GMOnly bool
	Handle Handler
	// Autocomplete is optional. A GM-only command returns empty choices to a
	// non-operator so a command's option names never leak (handled by dispatch).
	Autocomplete AutocompleteHandler
}

// Registry holds the slash-command surface: it produces the per-Guild command
// definitions and dispatches inbound interactions to the registered handler,
// authorizing each server-side via the Gate. It is the shared contract issues
// #108/#120/#211 register additional commands against.
type Registry struct {
	gate *Gate
	log  *slog.Logger

	// responseTimeout bounds a handler until it first responds (Discord's 3s
	// deadline); a Defer stops it so a slow deferred handler's own work is not
	// killed. Field (not the const) so tests drive it with a short deadline.
	responseTimeout time.Duration

	mu    sync.RWMutex
	cmds  map[string]Command // dispatch key -> command
	order []string           // registration order, for deterministic Definitions
}

// NewRegistry builds an empty Registry over a Gate. Register commands at boot,
// before the presence opens its gateway.
func NewRegistry(gate *Gate, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{gate: gate, log: log, cmds: map[string]Command{}, responseTimeout: interactionTimeout}
}

// Register adds commands to the surface. Boot-time only (before the first
// Ensure); the dispatch key is the command's Path.
func (r *Registry) Register(cmds ...Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range cmds {
		if _, dup := r.cmds[c.Path]; !dup {
			r.order = append(r.order, c.Path)
		}
		r.cmds[c.Path] = c
	}
}

// Definitions is the per-Guild registration payload: flat commands as their own
// SlashCommandCreate, and every "glyphoxa <sub>" merged into ONE /glyphoxa
// command carrying each sub as a SubCommand option (ADR-0010). Flat commands
// come first, then the merged group, in registration order.
func (r *Registry) Definitions() []discord.ApplicationCommandCreate {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var flat []discord.ApplicationCommandCreate
	group := discord.SlashCommandCreate{Name: commandGroup, Description: commandGroupDescription}
	haveGroup := false

	for _, path := range r.order {
		c := r.cmds[path]
		prefix, sub, isSub := strings.Cut(path, " ")
		if !isSub {
			flat = append(flat, discord.SlashCommandCreate{
				Name:        path,
				Description: c.Description,
				Options:     c.Options,
			})
			continue
		}
		if prefix != commandGroup {
			// Only the /glyphoxa group exists in v1.0; a stray grouped Path is a
			// programming error, but drop it rather than emit a bad command.
			r.log.Warn("presence: ignoring command with unknown group prefix", "path", path)
			continue
		}
		haveGroup = true
		group.Options = append(group.Options, discord.ApplicationCommandOptionSubCommand{
			Name:        sub,
			Description: c.Description,
			Options:     c.Options,
		})
	}

	defs := make([]discord.ApplicationCommandCreate, 0, len(flat)+1)
	defs = append(defs, flat...)
	if haveGroup {
		defs = append(defs, group)
	}
	return defs
}

// HandleCommand is the disgo listener for slash-command interactions. It builds
// an Interaction over the event and dispatches it; every path answers the
// interaction (a reply or an ephemeral error), never a silent drop.
func (r *Registry) HandleCommand(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		// We register only slash commands, so a non-slash application command is
		// never expected; ignore it rather than panic on the type assertion.
		return
	}
	ic := &Interaction{
		guildID: snowflakePtrString(e.GuildID()),
		userID:  e.User().ID.String(),
		opts:    data,
		resp:    &eventResponder{event: e},
	}
	r.dispatch(context.Background(), dispatchKey(data.CommandName(), data.SubCommandName), ic)
}

// dispatch is the transport-agnostic command core: look up, authorize, run. It
// is separated from HandleCommand so it can be unit-tested with a fake
// Interaction (a fake responder + fake options), no live Discord event needed.
func (r *Registry) dispatch(base context.Context, key string, ic *Interaction) {
	cmd, ok := r.lookup(key)
	if !ok {
		_ = ic.ReplyEphemeral("Unknown command.")
		r.log.Warn("presence: unknown slash command", "command", key)
		return
	}
	if err := r.authorize(cmd, ic.guildID, ic.userID); err != nil {
		_ = ic.ReplyEphemeral(gateMessage(err))
		return
	}
	// Bound the handler by Discord's ~3s first-response deadline via a watchdog
	// that cancels the ctx. A Defer (which ACKs within that window and opens the
	// minutes-long follow-up window) stops the watchdog so a slow deferred handler
	// — #120's transcript search — is not killed at the deadline. After a Defer,
	// ic routes replies through Followup, so a handler error still reaches the
	// user instead of a Discord 40060 ("already acknowledged") on CreateMessage.
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	watchdog := time.AfterFunc(r.responseTimeout, cancel)
	defer watchdog.Stop()
	ic.onDefer = func() { watchdog.Stop() }
	if err := cmd.Handle(ctx, ic); err != nil {
		r.log.Error("presence: slash command handler failed", "command", key, "err", err)
		_ = ic.ReplyEphemeral("Something went wrong handling that command.")
	}
}

// HandleAutocomplete is the disgo listener for autocomplete interactions. It
// always responds with a choice slice (possibly empty), so a focused option
// never hangs.
func (r *Registry) HandleAutocomplete(e *events.AutocompleteInteractionCreate) {
	data := e.Data
	ac := &Autocomplete{
		guildID: snowflakePtrString(e.GuildID()),
		userID:  e.User().ID.String(),
		data:    data,
	}
	choices := r.autocompleteChoices(context.Background(), dispatchKey(data.CommandName, data.SubCommandName), ac)
	_ = e.AutocompleteResult(choices)
}

// autocompleteChoices is the testable autocomplete core: it returns the handler
// choices, or an EMPTY (never nil) slice when the command is unknown, has no
// autocomplete, the invoker is not authorized (no name leak), or the handler
// fails.
func (r *Registry) autocompleteChoices(base context.Context, key string, ac *Autocomplete) []discord.AutocompleteChoice {
	empty := []discord.AutocompleteChoice{}
	cmd, ok := r.lookup(key)
	if !ok || cmd.Autocomplete == nil {
		return empty
	}
	if err := r.authorize(cmd, ac.guildID, ac.userID); err != nil {
		return empty
	}
	ctx, cancel := context.WithTimeout(base, r.responseTimeout)
	defer cancel()
	choices, err := cmd.Autocomplete(ctx, ac)
	if err != nil {
		r.log.Error("presence: autocomplete handler failed", "command", key, "err", err)
		return empty
	}
	if choices == nil {
		return empty
	}
	return choices
}

// authorize applies the command's server-side permission rule: GM-only commands
// require an allowlisted operator in the configured Guild, others just require
// the configured Guild.
func (r *Registry) authorize(cmd Command, guildID, userID string) error {
	if cmd.GMOnly {
		return r.gate.CheckGM(guildID, userID)
	}
	return r.gate.CheckGuild(guildID)
}

func (r *Registry) lookup(key string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cmds[key]
	return c, ok
}

// dispatchKey is the map key for an inbound interaction: the flat command name,
// or "<name> <sub>" for a grouped subcommand — matching Command.Path.
func dispatchKey(name string, sub *string) string {
	if sub != nil {
		return name + " " + *sub
	}
	return name
}

// gateMessage maps a Gate denial to its distinct ephemeral text.
func gateMessage(err error) string {
	switch {
	case errors.Is(err, ErrNotOperator):
		return "You're not authorized to use this command."
	case errors.Is(err, ErrWrongGuild):
		return "This command isn't available here."
	default:
		return "You can't use this command."
	}
}

// Interaction is the handler's view of one slash-command interaction: its option
// values, the invoker's identity, and the reply methods. The response path is
// injectable (responder) so the Registry dispatches in unit tests without a live
// Discord connection.
type Interaction struct {
	guildID string
	userID  string
	opts    optionSource
	resp    responder
	// deferred is set once Defer succeeds: after it, Reply/ReplyEphemeral route
	// through the post-Defer path, because the interaction is already acknowledged
	// and a fresh CreateMessage would be a Discord 40060 ("already acknowledged").
	deferred bool
	// originalConsumed is set once the deferred "thinking…" placeholder has been
	// resolved by the first post-Defer reply. It drives the registry-wide routing rule
	// (#335): the FIRST post-Defer reply edits the placeholder, every later one is a
	// fresh Followup.
	originalConsumed bool
	// onDefer is installed by dispatch to stop the first-response watchdog when
	// the handler Defers; nil when the Interaction is invoked outside dispatch.
	onDefer func()
}

// GuildID is the Guild the interaction happened in, or "" for a DM.
func (ic *Interaction) GuildID() string { return ic.guildID }

// UserID is the invoking Discord User's snowflake.
func (ic *Interaction) UserID() string { return ic.userID }

// String reads a string option by name; ok is false when it was not supplied.
func (ic *Interaction) String(name string) (string, bool) {
	if ic.opts == nil {
		return "", false
	}
	return ic.opts.OptString(name)
}

// Int reads an integer option by name; ok is false when it was not supplied.
func (ic *Interaction) Int(name string) (int64, bool) {
	if ic.opts == nil {
		return 0, false
	}
	v, ok := ic.opts.OptInt(name)
	return int64(v), ok
}

// Reply answers the interaction with a public in-channel message (a Followup
// once the interaction has been Deferred).
func (ic *Interaction) Reply(content string) error { return ic.reply(content, false) }

// ReplyEphemeral answers with a message only the invoker sees (a Followup once
// Deferred).
func (ic *Interaction) ReplyEphemeral(content string) error { return ic.reply(content, true) }

// reply routes a message to the correct Discord response call: a fresh
// CreateMessage before a Defer, the post-Defer path after (a post-ACK CreateMessage
// is a 40060). This makes both a handler's domain-error reply and the Registry's
// generic-error reply reach the user after a Defer.
func (ic *Interaction) reply(content string, ephemeral bool) error {
	if ic.deferred {
		return ic.sendPostDefer(content, ephemeral)
	}
	return ic.resp.reply(content, ephemeral)
}

// sendPostDefer is the registry-wide post-Defer routing rule (#335). Discord
// DEPRECATED the shim where the first CreateFollowupMessage after a deferred
// response implicitly edited the "thinking…" placeholder; a followup now always
// creates a fresh message, leaving the placeholder dangling. So the FIRST post-Defer
// reply resolves the placeholder via EditOriginal (its visibility fixed to the
// Defer's), and every later one is a real Followup honoring its own flag. Owned here
// at the Interaction level so every command — not just recap's public path — routes
// identically.
func (ic *Interaction) sendPostDefer(content string, ephemeral bool) error {
	if !ic.originalConsumed {
		// Mark consumed only AFTER the edit succeeds: if Discord 5xxs the edit the
		// placeholder is still unresolved, so the retry (the dispatch generic-error
		// ReplyEphemeral) must edit again — not route to a followup that would leave the
		// "thinking…" placeholder dangling forever.
		if err := ic.resp.editOriginal(content); err != nil {
			return err
		}
		ic.originalConsumed = true
		return nil
	}
	return ic.resp.followup(content, ephemeral)
}

// Defer acknowledges the interaction with a "thinking…" placeholder, buying a
// slow handler time past the 3s deadline; it later sends the real reply with
// Followup (or Reply, which routes to Followup once deferred). Defer also stops
// the dispatch first-response watchdog so the handler's own work is not killed.
func (ic *Interaction) Defer(ephemeral bool) error {
	if err := ic.resp.deferResponse(ephemeral); err != nil {
		return err
	}
	ic.deferred = true
	if ic.onDefer != nil {
		ic.onDefer()
	}
	return nil
}

// Followup sends a message after a Defer. It obeys the registry-wide post-Defer rule
// (#335): the FIRST post-Defer message resolves the deferred placeholder via
// EditOriginal (its visibility fixed to the Defer's), and later ones create fresh
// messages honoring their own flag. So a handler no longer has to EditOriginal by
// hand before a public Followup — it just replies, and the first reply is the
// placeholder edit. Called before a Defer it falls back to a plain followup.
func (ic *Interaction) Followup(content string, ephemeral bool) error {
	if ic.deferred {
		return ic.sendPostDefer(content, ephemeral)
	}
	return ic.resp.followup(content, ephemeral)
}

// Autocomplete is the handler's view of one autocomplete interaction.
type Autocomplete struct {
	guildID string
	userID  string
	data    discord.AutocompleteInteractionData
}

// Focused is the option the user is currently typing (its name and partial
// value). The value is decoded defensively: disgo's own accessor panics on a
// non-string or absent value, but an autocomplete can fire before any character
// is typed, so this returns "" rather than crash the gateway goroutine.
func (ac *Autocomplete) Focused() (name, value string) {
	f := ac.data.Focused()
	if len(f.Value) == 0 {
		return f.Name, ""
	}
	var s string
	if err := json.Unmarshal(f.Value, &s); err == nil {
		return f.Name, s
	}
	return f.Name, strings.TrimSpace(string(f.Value))
}

// UserID is the invoking Discord User's snowflake.
func (ac *Autocomplete) UserID() string { return ac.userID }

// GuildID is the Guild the interaction happened in, or "" for a DM.
func (ac *Autocomplete) GuildID() string { return ac.guildID }

// optionSource is the option-reading surface an Interaction needs;
// discord.SlashCommandInteractionData satisfies it in production, a fake map in
// tests.
type optionSource interface {
	OptString(name string) (string, bool)
	OptInt(name string) (int, bool)
}

// responder is the injectable interaction-response sink: production wraps the
// disgo event, tests record the calls.
type responder interface {
	reply(content string, ephemeral bool) error
	deferResponse(ephemeral bool) error
	followup(content string, ephemeral bool) error
	editOriginal(content string) error
}

// eventResponder is the production responder over a live slash-command event.
type eventResponder struct {
	event *events.ApplicationCommandInteractionCreate
}

func (r *eventResponder) reply(content string, ephemeral bool) error {
	return r.event.CreateMessage(ephemeralMessage(content, ephemeral))
}

func (r *eventResponder) deferResponse(ephemeral bool) error {
	return r.event.DeferCreateMessage(ephemeral)
}

func (r *eventResponder) followup(content string, ephemeral bool) error {
	_, err := r.event.Client().Rest.CreateFollowupMessage(
		r.event.ApplicationID(), r.event.Token(), ephemeralMessage(content, ephemeral))
	return err
}

// editOriginal edits the original interaction response — the deferred placeholder —
// in place (Discord's Edit Original Interaction Response). It carries no ephemeral
// flag: the original's visibility (set at Defer time) is preserved regardless.
func (r *eventResponder) editOriginal(content string) error {
	_, err := r.event.Client().Rest.UpdateInteractionResponse(
		r.event.ApplicationID(), r.event.Token(), discord.MessageUpdate{Content: &content})
	return err
}

// ephemeralMessage builds a MessageCreate, flagged ephemeral when requested.
func ephemeralMessage(content string, ephemeral bool) discord.MessageCreate {
	mc := discord.MessageCreate{Content: content}
	if ephemeral {
		mc.Flags = mc.Flags.Add(discord.MessageFlagEphemeral)
	}
	return mc
}

// snowflakePtrString renders an optional Guild snowflake as a string, "" for a
// nil (DM) pointer.
func snowflakePtrString(id *snowflake.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// compile-time proof the production responder satisfies the seam.
var _ responder = (*eventResponder)(nil)

// compile-time proof disgo's slash data satisfies the option seam.
var _ optionSource = discord.SlashCommandInteractionData{}
