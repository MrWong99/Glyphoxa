package commands

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

// voiceRecapColor is the embed sidebar color for voice recap embeds.
const voiceRecapColor = 0x9B59B6

// VoiceRecapCommands handles the /session voice-recap slash command.
type VoiceRecapCommands struct {
	sessionMgr   *app.SessionManager
	perms        *discordbot.PermissionChecker
	generator    *session.RecapGenerator
	recapStore   memory.RecapStore
	sessionStore memory.SessionStore
	npcs         []config.NPCConfig
}

// VoiceRecapConfig holds dependencies for creating VoiceRecapCommands.
type VoiceRecapConfig struct {
	Bot          *discordbot.Bot
	SessionMgr   *app.SessionManager
	Perms        *discordbot.PermissionChecker
	Generator    *session.RecapGenerator
	RecapStore   memory.RecapStore
	SessionStore memory.SessionStore
	NPCs         []config.NPCConfig
}

// NewVoiceRecapCommands creates a VoiceRecapCommands and registers the handler.
func NewVoiceRecapCommands(cfg VoiceRecapConfig) *VoiceRecapCommands {
	vrc := &VoiceRecapCommands{
		sessionMgr:   cfg.SessionMgr,
		perms:        cfg.Perms,
		generator:    cfg.Generator,
		recapStore:   cfg.RecapStore,
		sessionStore: cfg.SessionStore,
		npcs:         cfg.NPCs,
	}
	cfg.Bot.Router().RegisterHandler("session/voice-recap", vrc.handleVoiceRecap)
	return vrc
}

// gmHelperVoice returns the VoiceProfile of the GM helper NPC.
// Falls back to the first NPC if no GM helper is designated.
func (vrc *VoiceRecapCommands) gmHelperVoice() tts.VoiceProfile {
	for _, npc := range vrc.npcs {
		if npc.GMHelper {
			return tts.VoiceProfile{
				ID:          npc.Voice.VoiceID,
				Provider:    npc.Voice.Provider,
				PitchShift:  npc.Voice.PitchShift,
				SpeedFactor: npc.Voice.SpeedFactor,
			}
		}
	}
	// Fallback to first NPC.
	if len(vrc.npcs) > 0 {
		npc := vrc.npcs[0]
		return tts.VoiceProfile{
			ID:          npc.Voice.VoiceID,
			Provider:    npc.Voice.Provider,
			PitchShift:  npc.Voice.PitchShift,
			SpeedFactor: npc.Voice.SpeedFactor,
		}
	}
	return tts.VoiceProfile{}
}

// handleVoiceRecap handles /session voice-recap.
func (vrc *VoiceRecapCommands) handleVoiceRecap(e *events.ApplicationCommandInteractionCreate) {
	if !vrc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to generate voice recaps.")
		return
	}

	if !vrc.sessionMgr.IsActive() {
		discordbot.RespondEphemeral(e, "A voice session must be active to play the recap. Start one with `/session start`.")
		return
	}

	// Resolve session ID.
	var sessionID string
	data := e.SlashCommandInteractionData()
	if opt, ok := data.OptString("session_id"); ok && opt != "" {
		sessionID = opt
	}

	if sessionID == "" && vrc.sessionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := vrc.sessionStore.ListSessions(ctx, 1)
		if err == nil && len(sessions) > 0 {
			sessionID = sessions[0].SessionID
		}
	}

	if sessionID == "" {
		discordbot.RespondEphemeral(e, "No previous session found for this campaign.")
		return
	}

	discordbot.DeferReply(e)

	// Check cache.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var recap *memory.Recap
	if vrc.recapStore != nil {
		cached, err := vrc.recapStore.GetRecap(ctx, sessionID)
		if err != nil {
			slog.Warn("voice-recap: cache lookup failed", "session_id", sessionID, "err", err)
		}
		if cached != nil {
			recap = cached
		}
	}

	// Generate if not cached.
	if recap == nil {
		if vrc.generator == nil {
			discordbot.FollowUp(e, "Voice recaps require LLM and TTS providers to be configured.")
			return
		}

		voice := vrc.gmHelperVoice()
		info := vrc.sessionMgr.Info()

		generated, err := vrc.generator.Generate(ctx, sessionID, info.CampaignName, vrc.sessionStore, voice)
		if err != nil {
			slog.Error("voice-recap: generation failed", "session_id", sessionID, "err", err)
			discordbot.FollowUp(e, fmt.Sprintf("Failed to generate voice recap: %v", err))
			return
		}
		recap = generated
	}

	// Play audio via mixer.
	mixer := vrc.sessionMgr.Mixer()
	if mixer == nil {
		discordbot.FollowUp(e, "Session is not fully active. Cannot play audio.")
		return
	}

	audioCh := make(chan []byte, 1)
	go func() {
		defer close(audioCh)
		audioCh <- recap.AudioData
	}()

	segment := &audio.AudioSegment{
		NPCID:      "narrator",
		Audio:      audioCh,
		SampleRate: recap.SampleRate,
		Channels:   recap.Channels,
		Priority:   10, // high priority — recap should not be interrupted
	}

	mixer.Enqueue(segment, segment.Priority)

	// Post text embed alongside.
	now := time.Now().UTC()
	embed := discord.Embed{
		Title:       "Previously, on your campaign...",
		Description: recap.Text,
		Color:       voiceRecapColor,
		Footer:      &discord.EmbedFooter{Text: fmt.Sprintf("Session: %s | Duration: %s", sessionID, recap.Duration.Truncate(time.Second))},
		Timestamp:   &now,
	}
	discordbot.FollowUpEmbed(e, embed)
}
