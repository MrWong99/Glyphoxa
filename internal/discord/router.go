package discord

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// CommandFunc is the signature for slash command handlers.
type CommandFunc func(e *events.ApplicationCommandInteractionCreate)

// AutocompleteHandlerFunc is the signature for autocomplete handlers.
type AutocompleteHandlerFunc func(e *events.AutocompleteInteractionCreate)

// ComponentFunc is the signature for component interaction handlers (buttons, selects).
type ComponentFunc func(e *events.ComponentInteractionCreate)

// ModalFunc is the signature for modal submit handlers.
type ModalFunc func(e *events.ModalSubmitInteractionCreate)

// commandEntry stores a command definition along with its handler.
type commandEntry struct {
	command discord.SlashCommandCreate
	handler CommandFunc
	hasDef  bool // true if command definition was provided
}

// CommandRouter dispatches Discord interactions to registered handlers.
type CommandRouter struct {
	mu              sync.RWMutex
	commands        map[string]commandEntry            // "command" or "command/subcommand" → entry
	autocomplete    map[string]AutocompleteHandlerFunc // "command" or "command/subcommand" → handler
	components      map[string]ComponentFunc           // custom_id → handler
	componentPrefix map[string]ComponentFunc           // prefix → handler (for dynamic suffixes)
	modals          map[string]ModalFunc               // custom_id → handler
}

// NewCommandRouter creates an empty router.
func NewCommandRouter() *CommandRouter {
	return &CommandRouter{
		commands:        make(map[string]commandEntry),
		autocomplete:    make(map[string]AutocompleteHandlerFunc),
		components:      make(map[string]ComponentFunc),
		componentPrefix: make(map[string]ComponentFunc),
		modals:          make(map[string]ModalFunc),
	}
}

// RegisterCommand registers a handler for a slash command. The key format is
// "command" or "command/subcommand" (e.g., "npc/mute"). The cmd definition
// is used when registering commands with Discord (only top-level commands are
// registered; subcommands are nested inside).
func (r *CommandRouter) RegisterCommand(key string, cmd discord.SlashCommandCreate, handler CommandFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[key] = commandEntry{command: cmd, handler: handler, hasDef: true}
}

// RegisterHandler registers a handler for a slash command key without
// providing a command definition. Use this for subcommand handlers when
// the parent command is already registered.
func (r *CommandRouter) RegisterHandler(key string, handler CommandFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[key] = commandEntry{handler: handler}
}

// RegisterAutocomplete registers an autocomplete handler.
func (r *CommandRouter) RegisterAutocomplete(key string, handler AutocompleteHandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autocomplete[key] = handler
}

// RegisterComponent registers a handler for a message component interaction (buttons).
func (r *CommandRouter) RegisterComponent(customID string, handler ComponentFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.components[customID] = handler
}

// RegisterComponentPrefix registers a handler that matches any component
// whose custom_id starts with the given prefix. This is useful for buttons
// with dynamic suffixes (e.g., "entity_remove_confirm:" matches
// "entity_remove_confirm:some-id").
func (r *CommandRouter) RegisterComponentPrefix(prefix string, handler ComponentFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.componentPrefix[prefix] = handler
}

// RegisterModal registers a handler for a modal submit interaction.
func (r *CommandRouter) RegisterModal(customID string, handler ModalFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modals[customID] = handler
}

// ApplicationCommands returns the deduplicated list of top-level command
// definitions for registration with the Discord API.
func (r *CommandRouter) ApplicationCommands() []discord.ApplicationCommandCreate {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var cmds []discord.ApplicationCommandCreate
	for _, entry := range r.commands {
		if entry.hasDef && !seen[entry.command.Name] {
			seen[entry.command.Name] = true
			cmds = append(cmds, entry.command)
		}
	}
	return cmds
}

// HandleCommand dispatches an application command interaction.
func (r *CommandRouter) HandleCommand(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		slog.Warn("discord: non-slash command interaction", "type", e.Data.Type())
		return
	}
	key := commandKey(data)

	r.mu.RLock()
	entry, found := r.commands[key]
	r.mu.RUnlock()

	if !found {
		slog.Warn("discord: unknown command", "key", key)
		RespondEphemeral(e, "Unknown command.")
		return
	}
	entry.handler(e)
}

// HandleAutocomplete dispatches an autocomplete interaction.
func (r *CommandRouter) HandleAutocomplete(e *events.AutocompleteInteractionCreate) {
	data := e.Data
	key := autocompleteKey(data)

	r.mu.RLock()
	handler, found := r.autocomplete[key]
	r.mu.RUnlock()

	if !found {
		slog.Debug("discord: no autocomplete handler", "key", key)
		_ = e.AutocompleteResult(nil)
		return
	}
	handler(e)
}

// HandleComponent dispatches a component interaction.
func (r *CommandRouter) HandleComponent(e *events.ComponentInteractionCreate) {
	customID := e.Data.CustomID()

	r.mu.RLock()
	handler, ok := r.components[customID]
	if !ok {
		for prefix, h := range r.componentPrefix {
			if strings.HasPrefix(customID, prefix) {
				handler = h
				ok = true
				break
			}
		}
	}
	r.mu.RUnlock()

	if !ok {
		slog.Warn("discord: unknown component", "custom_id", customID)
		RespondEphemeral(e, "Unknown component.")
		return
	}
	handler(e)
}

// HandleModal dispatches a modal submit interaction.
func (r *CommandRouter) HandleModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID

	r.mu.RLock()
	handler, ok := r.modals[customID]
	r.mu.RUnlock()

	if !ok {
		slog.Warn("discord: unknown modal", "custom_id", customID)
		RespondEphemeral(e, "Unknown modal.")
		return
	}
	handler(e)
}

// commandKey builds a router key from a slash command interaction data.
func commandKey(data discord.SlashCommandInteractionData) string {
	key := data.CommandName()
	if data.SubCommandName != nil {
		key += "/" + *data.SubCommandName
	}
	return key
}

// autocompleteKey builds a router key from an autocomplete interaction data.
func autocompleteKey(data discord.AutocompleteInteractionData) string {
	key := data.CommandName
	if data.SubCommandName != nil {
		key += "/" + *data.SubCommandName
	}
	return key
}
