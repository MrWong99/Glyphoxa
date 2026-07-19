package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Highlight Discord delivery (#310, Epic 8, ADR-0051 GM-only sharing): ShareHighlight
// posts a promoted Highlight's clip as a file to a text channel OR replays it into
// the live voice channel, on explicit GM action (no auto-post). ListShareChannels
// backs the share dialog's channel picker. Both are tenant- AND Active-Campaign-
// scoped exactly like the other Highlight RPCs (the errNoSuchHighlight posture).

// ErrNoDiscordToken is the sentinel a [HighlightSharer] returns when no Discord Bot
// token is saved, so the RPC maps it to a readable CodeFailedPrecondition ("save a
// Discord Bot token first") — the ResolveGuildInvite posture. It is distinct from a
// Discord API failure (which is CodeUnavailable).
var ErrNoDiscordToken = errors.New("rpc: no Discord bot token saved")

// captionMaxRunes bounds the message caption (the Highlight excerpt) so a long
// excerpt cannot overrun Discord's 2000-char message limit; the headroom leaves
// room for Discord's own formatting.
const captionMaxRunes = 1900

// HighlightSharer posts a Highlight clip into Discord (#310). The concrete impl
// resolves the Bot token + guild from the deployment config internally (a small
// service, see DeploymentSharer); the RPC layer holds only this seam so unit tests
// fake it and never touch the network. A missing saved token is [ErrNoDiscordToken].
type HighlightSharer interface {
	// ListTextChannels lists the guild's text channels the GM may post into.
	ListTextChannels(ctx context.Context) ([]discordshare.Channel, error)
	// PostClip uploads data as a file to channelID with caption as the message text.
	PostClip(ctx context.Context, channelID, caption, filename, contentType string, data []byte) error
}

// HighlightReplayer replays a Highlight clip into the live voice channel (#310).
// *session.Manager satisfies it; a replay with no live session is
// [session.ErrNoActiveSession].
type HighlightReplayer interface {
	ReplayHighlight(ctx context.Context, tenantID uuid.UUID, clipKey string) error
}

// ShareChannelStore reads/writes the Campaign's remembered share channel (#310) so
// the dialog pre-selects the last choice. *storage.Store satisfies it.
type ShareChannelStore interface {
	GetCampaignShareChannel(ctx context.Context, campaignID uuid.UUID) (string, error)
	SetCampaignShareChannel(ctx context.Context, campaignID uuid.UUID, channelID string) error
}

// SetSharing wires the Highlight Discord-delivery seam onto the SessionServer
// (#310). Called once at boot after the sharer/replayer/store are built. Unwired,
// ShareHighlight / ListShareChannels report CodeUnimplemented.
func (s *SessionServer) SetSharing(sharer HighlightSharer, replayer HighlightReplayer, shareStore ShareChannelStore) {
	s.sharer = sharer
	s.replayer = replayer
	s.shareStore = shareStore
}

// sharingEnabled reports CodeUnimplemented when the sharing seam was not wired
// (web-standalone tests, keyless boots), mirroring highlightsEnabled.
func (s *SessionServer) sharingEnabled() error {
	if s.sharer == nil || s.shareStore == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("highlight sharing is not enabled on this server"))
	}
	return nil
}

// ListShareChannels lists the deployment guild's text channels for the share dialog
// plus the Active Campaign's last-chosen channel (#310). A read. A missing saved Bot
// token is CodeFailedPrecondition ("save a Discord Bot token first").
func (s *SessionServer) ListShareChannels(
	ctx context.Context,
	_ *connect.Request[managementv1.ListShareChannelsRequest],
) (*connect.Response[managementv1.ListShareChannelsResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	if err := s.sharingEnabled(); err != nil {
		return nil, err
	}
	if _, err := s.tenant(ctx); err != nil {
		return nil, err
	}

	channels, err := s.sharer.ListTextChannels(ctx)
	if errors.Is(err, ErrNoDiscordToken) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("save a Discord Bot token first"))
	}
	if err != nil {
		return nil, s.discordError("ListShareChannels", err)
	}

	// The Active Campaign's last choice pre-selects the dialog; a resolve failure or
	// a campaign with none yet leaves it empty (never blocks the list).
	last := ""
	if campaignID, ok, cerr := s.searchCampaign(ctx); cerr == nil && ok {
		if ch, gerr := s.shareStore.GetCampaignShareChannel(ctx, campaignID); gerr == nil {
			last = ch
		} else if !errors.Is(gerr, storage.ErrNotFound) {
			s.log.Error("ListShareChannels: read last share channel failed", "err", gerr)
		}
	}

	out := make([]*managementv1.TextChannel, 0, len(channels))
	for _, c := range channels {
		out = append(out, &managementv1.TextChannel{Id: c.ID, Name: c.Name})
	}
	return connect.NewResponse(&managementv1.ListShareChannelsResponse{
		Channels:           out,
		LastShareChannelId: last,
	}), nil
}

// ShareHighlight delivers a promoted Highlight into Discord (#310): a file to a text
// channel or a voice replay. Only a promoted Highlight is shareable; an oversize
// clip is refused before any blob fetch (no re-encode); a missing/foreign/cross-
// campaign id is CodeNotFound (the errNoSuchHighlight posture).
func (s *SessionServer) ShareHighlight(
	ctx context.Context,
	req *connect.Request[managementv1.ShareHighlightRequest],
) (*connect.Response[managementv1.ShareHighlightResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	if err := s.sharingEnabled(); err != nil {
		return nil, err
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	id, perr := uuid.Parse(req.Msg.GetId())
	if perr != nil {
		return nil, errNoSuchHighlight()
	}
	campaignID, err := s.activeCampaignForHighlight(ctx)
	if err != nil {
		return nil, err
	}

	h, gerr := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(gerr, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if gerr != nil {
		s.log.Error("ShareHighlight: load row failed", "err", gerr)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if h.CampaignID != campaignID {
		return nil, errNoSuchHighlight() // cross-campaign: never leak that it exists
	}
	// Only a promoted Highlight is shareable (#310 decision, ADR-0051): a candidate
	// is refused so an un-kept moment never leaves the instance.
	if h.Status != storage.HighlightPromoted {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only promoted highlights can be shared"))
	}

	switch req.Msg.GetMode().(type) {
	case *managementv1.ShareHighlightRequest_TextChannelId:
		return s.shareToChannel(ctx, campaignID, h, req.Msg.GetTextChannelId())
	case *managementv1.ShareHighlightRequest_VoiceReplay:
		return s.replayToVoice(ctx, h)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a share mode is required"))
	}
}

// shareToChannel posts the clip as a file to channelID and remembers the choice.
func (s *SessionServer) shareToChannel(
	ctx context.Context,
	campaignID uuid.UUID,
	h storage.Highlight,
	channelID string,
) (*connect.Response[managementv1.ShareHighlightResponse], error) {
	if channelID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a text channel is required"))
	}
	// Refuse an oversize clip BEFORE fetching the blob (the sharer is never called):
	// v1 refuses, never re-encodes (#310 decision).
	if h.ClipSizeBytes > discordshare.MaxUploadBytes {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"clip is %.1f MB; Discord upload limit is %d MB",
			float64(h.ClipSizeBytes)/(1<<20), discordshare.MaxUploadBytes/(1<<20)))
	}

	rc, _, berr := s.blobs.Get(ctx, h.ClipKey)
	if berr != nil {
		s.log.Error("ShareHighlight: fetch clip failed", "err", berr, "highlight", h.ID)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// The size pre-check trusted the row's clip_size_bytes; the blob is authoritative,
	// so cap the read too — a STALE small row must not stream a huge blob into memory
	// and up to Discord. Read one byte past the limit: if it fills, the true clip is
	// oversize and we refuse (never send), same posture as the row pre-check.
	data, rerr := io.ReadAll(io.LimitReader(rc, discordshare.MaxUploadBytes+1))
	_ = rc.Close()
	if rerr != nil {
		s.log.Error("ShareHighlight: read clip failed", "err", rerr, "highlight", h.ID)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if int64(len(data)) > discordshare.MaxUploadBytes {
		s.log.Warn("ShareHighlight: clip blob exceeds upload limit (stale row size)", "highlight", h.ID, "rowBytes", h.ClipSizeBytes)
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"clip exceeds Discord's %d MB upload limit", discordshare.MaxUploadBytes/(1<<20)))
	}

	caption := truncateRunes(h.Excerpt, captionMaxRunes)
	if perr := s.sharer.PostClip(ctx, channelID, caption, "highlight.wav", h.ClipContentType, data); perr != nil {
		if errors.Is(perr, ErrNoDiscordToken) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("save a Discord Bot token first"))
		}
		return nil, s.discordError("ShareHighlight", perr)
	}

	// Remember the channel for the dialog's next pre-selection. A persist failure is
	// logged only — the share already succeeded, so it must not fail the RPC.
	if serr := s.shareStore.SetCampaignShareChannel(ctx, campaignID, channelID); serr != nil {
		s.log.Error("ShareHighlight: remember share channel failed", "err", serr, "campaign", campaignID)
	}
	return connect.NewResponse(&managementv1.ShareHighlightResponse{}), nil
}

// replayToVoice replays the clip into the live voice channel (#310). No live Voice
// Session is CodeFailedPrecondition.
func (s *SessionServer) replayToVoice(
	ctx context.Context,
	h storage.Highlight,
) (*connect.Response[managementv1.ShareHighlightResponse], error) {
	if s.replayer == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("voice replay is not enabled on this server"))
	}
	// Replay into the Highlight's OWN Tenant's live session (#488): the Highlight was
	// already campaign-ownership-checked to the caller's Tenant, so h.TenantID is the
	// operator's Tenant — the session the clip belongs in.
	if err := s.replayer.ReplayHighlight(ctx, h.TenantID, h.ClipKey); err != nil {
		if errors.Is(err, session.ErrSplitMode) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errSplitMode)
		}
		if errors.Is(err, session.ErrNoActiveSession) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no live Voice Session to replay into"))
		}
		s.log.Error("ShareHighlight: replay failed", "err", err, "highlight", h.ID)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.ShareHighlightResponse{}), nil
}

// discordError maps a Discord REST failure to a readable CodeUnavailable so the GM
// sees "Discord rejected the upload" rather than a raw transport error. A non-API
// error (build/transport) is CodeInternal.
func (s *SessionServer) discordError(op string, err error) *connect.Error {
	var apiErr *discordshare.APIError
	if errors.As(err, &apiErr) {
		s.log.Warn(op+": Discord API error", "status", apiErr.Status)
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("the Discord request was rejected (HTTP %d)", apiErr.Status))
	}
	if errors.Is(err, discordshare.ErrNoAccess) {
		return connect.NewError(connect.CodeUnavailable, errors.New("the Bot cannot access that Discord guild"))
	}
	s.log.Error(op+": Discord call failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// truncateRunes clamps s to at most n runes (rune-safe, never splits a codepoint).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
