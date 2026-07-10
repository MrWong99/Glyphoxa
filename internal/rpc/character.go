package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Player Character (PC) CRUD handlers (#276, E4) on CampaignServer. Like the Agent
// CRUD they resolve the single operator's active campaign server-side (ADR-0039),
// so another campaign's Characters are never returned nor mutable. discord_user_id
// is mandatory (ADR-0003) and must be a Discord snowflake; a Discord User plays at
// most one Character per campaign, so a duplicate is CodeAlreadyExists and a
// rebind is an ordinary update of that column.

// ListCharacters returns the active campaign's Player Characters in storage
// display order. No campaign is CodeNotFound; a storage failure is CodeInternal.
func (s *CampaignServer) ListCharacters(
	ctx context.Context,
	_ *connect.Request[managementv1.ListCharactersRequest],
) (*connect.Response[managementv1.ListCharactersResponse], error) {
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListCharacters: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	chars, err := s.store.ListCharacters(ctx, c.ID)
	if err != nil {
		slog.Default().Error("ListCharacters: store list failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Character, 0, len(chars))
	for _, ch := range chars {
		out = append(out, toProtoCharacter(ch))
	}
	return connect.NewResponse(&managementv1.ListCharactersResponse{Characters: out}), nil
}

// CreateCharacter adds a Player Character to the active campaign and returns it. An
// empty name or a non-snowflake discord_user_id is CodeInvalidArgument; a Discord
// User already playing a Character in the campaign is CodeAlreadyExists; no
// campaign is CodeNotFound.
func (s *CampaignServer) CreateCharacter(
	ctx context.Context,
	req *connect.Request[managementv1.CreateCharacterRequest],
) (*connect.Response[managementv1.CreateCharacterResponse], error) {
	m := req.Msg
	name := strings.TrimSpace(m.GetName())
	discordUserID, err := validateCharacterFields(name, m.GetDiscordUserId())
	if err != nil {
		return nil, err
	}

	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateCharacter: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	aliases := m.GetAliases()
	id, err := s.store.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID:    c.ID,
		Name:          name,
		Aliases:       aliases,
		DiscordUserID: discordUserID,
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a character for that discord user already exists in this campaign"))
		}
		slog.Default().Error("CreateCharacter: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// A new speaker→Character mapping: drop the campaign's cached resolutions so the
	// live relay attributes this Discord User's next line to the new Character (#281).
	s.invalidateSpeakers(c.ID)
	return connect.NewResponse(&managementv1.CreateCharacterResponse{
		Character: &managementv1.Character{
			Id:            id.String(),
			Name:          name,
			Aliases:       aliases,
			DiscordUserId: discordUserID,
		},
	}), nil
}

// UpdateCharacter saves a Character's editor fields and returns the updated
// Character. Rebinding discord_user_id to a different Discord User is a normal
// update (it stays required). An unparsable id, empty name, or non-snowflake
// discord_user_id is CodeInvalidArgument; an unknown id is CodeNotFound; a
// collision with another Character's Discord User is CodeAlreadyExists.
func (s *CampaignServer) UpdateCharacter(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateCharacterRequest],
) (*connect.Response[managementv1.UpdateCharacterResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid character id"))
	}
	name := strings.TrimSpace(m.GetName())
	discordUserID, err := validateCharacterFields(name, m.GetDiscordUserId())
	if err != nil {
		return nil, err
	}

	// Resolve the active campaign and scope the write to it (#342): the store's
	// UPDATE matches (id, campaign_id), so a Character in another campaign is never
	// mutable through this operator's session — it reads back as CodeNotFound.
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("UpdateCharacter: get active campaign failed", "character_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	updated, err := s.store.UpdateCharacter(ctx, storage.CharacterUpdate{
		ID:            id,
		CampaignID:    c.ID,
		Name:          name,
		Aliases:       m.GetAliases(),
		DiscordUserID: discordUserID,
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			return nil, connect.NewError(connect.CodeNotFound, errors.New("character not found"))
		case errors.Is(err, storage.ErrConflict):
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a character for that discord user already exists in this campaign"))
		default:
			slog.Default().Error("UpdateCharacter: store update failed", "character_id", id, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}
	// A rebind or rename changes how this Discord User resolves: drop the campaign's
	// cached resolutions so the next projected line reflects it (#281).
	s.invalidateSpeakers(updated.CampaignID)
	return connect.NewResponse(&managementv1.UpdateCharacterResponse{Character: toProtoCharacter(updated)}), nil
}

// DeleteCharacter removes a Player Character by id. An unparsable id is
// CodeInvalidArgument; a missing id is CodeNotFound; a storage failure is
// CodeInternal.
func (s *CampaignServer) DeleteCharacter(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteCharacterRequest],
) (*connect.Response[managementv1.DeleteCharacterResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid character id"))
	}

	// Resolve the active campaign and scope the delete to it (#342): the store's
	// DELETE matches (id, campaign_id), so another campaign's Character is never
	// removable through this session — it reads back as CodeNotFound.
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("DeleteCharacter: get active campaign failed", "character_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	switch err := s.store.DeleteCharacter(ctx, c.ID, id); {
	case err == nil:
		// The deleted mapping's Discord User now falls back to guild name / generic
		// label: drop the active campaign's cached resolutions (#281).
		s.invalidateSpeakers(c.ID)
		return connect.NewResponse(&managementv1.DeleteCharacterResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("character not found"))
	default:
		slog.Default().Error("DeleteCharacter: store delete failed", "character_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// validateCharacterFields enforces the shared create/update input rules: a
// non-empty (trimmed) name and a discord_user_id that is a Discord snowflake —
// decimal digits only (ADR-0003: the Discord User ID is the mandatory identity).
// It returns the validated discord_user_id or a CodeInvalidArgument error.
func validateCharacterFields(trimmedName, discordUserID string) (string, error) {
	if trimmedName == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	if !isSnowflake(discordUserID) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("discord_user_id must be a discord snowflake (digits only)"))
	}
	return discordUserID, nil
}

// isSnowflake reports whether s is a non-empty run of decimal digits — the shape
// of a Discord snowflake id. It rejects empty, signed, or non-numeric input
// before it ever reaches storage.
func isSnowflake(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// toProtoCharacter maps a storage.Character onto its wire representation. The
// nullable linked_user_id becomes "" when dormant (unset until Discord OAuth,
// ADR-0003).
func toProtoCharacter(c storage.Character) *managementv1.Character {
	pc := &managementv1.Character{
		Id:            c.ID.String(),
		Name:          c.Name,
		Aliases:       c.Aliases,
		DiscordUserId: c.DiscordUserID,
	}
	if c.LinkedUserID != nil {
		pc.LinkedUserId = *c.LinkedUserID
	}
	return pc
}
