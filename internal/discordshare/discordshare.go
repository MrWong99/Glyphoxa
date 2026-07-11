// Package discordshare posts a Session Highlight into Discord with the deployment
// Bot token (#310, Epic 8, ADR-0051 GM-only sharing): it lists a guild's text
// channels for the share dialog and uploads a clip as a message attachment — the
// codebase's first file-attachment path.
//
// Like internal/discordinvite and internal/discordtag, the calls are plain
// net/http against Discord's REST API, deliberately NOT disgo's rest.NewClient:
// that client's rate limiter starts a cleanup goroutine (`for range ticker.C`)
// its Close never stops, leaking one goroutine + ticker per call (ADR-0047).
//
// These are LIVE network calls. The RPC layer hides them behind a seam so unit
// tests fake the sharer and never touch the network; offline tests drive the
// package-private base-URL seam ([ListTextChannelsAt] / [PostFileAt]) at a fake
// HTTP server.
package discordshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"time"
)

// liveAPIBaseURL is Discord's REST API root, matching the version disgo pins and
// the sibling discordinvite package.
const liveAPIBaseURL = "https://discord.com/api/v10"

// userAgent is the DiscordBot form Discord requires of bot REST callers, matching
// internal/discordinvite / internal/discordtag.
const userAgent = "DiscordBot (https://github.com/MrWong99/Glyphoxa, v2)"

// guildTextType is Discord's channel type for a standard text channel
// (GUILD_TEXT). Voice (2), category (4), announcement (5), and stage (13) are
// excluded — a clip file goes to a text channel.
const guildTextType = 0

// MaxUploadBytes is the clip-size ceiling a share refuses above (#310 decision:
// refuse, never re-encode). It is the Discord unboosted attachment floor (8 MiB),
// a single deliberately conservative named constant so every layer — the RPC's
// pre-flight size check and the operator-facing error text — reads the same
// number. A boosted guild allows more, but sizing to the floor keeps a shared
// clip playable in ANY guild.
const MaxUploadBytes int64 = 8 << 20 // 8 MiB

// ErrNoAccess means the Bot cannot read the guild's channels (403/404 — Discord
// is inconsistent for a non-member), mirroring discordinvite.ErrNoAccess.
var ErrNoAccess = errors.New("discordshare: the Bot is not a member of that guild")

// APIError is a readable Discord REST failure: the HTTP status plus the operation,
// so the RPC can surface a clear CodeUnavailable ("Discord rejected the upload:
// HTTP 413") without leaking the token or the raw body.
type APIError struct {
	Op     string
	Status int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("discordshare: %s: HTTP %d", e.Op, e.Status)
}

// Channel is one guild text channel: its snowflake and name.
type Channel struct {
	ID   string
	Name string
}

// httpClient is shared across calls (http.Client is safe for concurrent use); its
// Timeout is a belt on top of the caller's ctx deadline so a call made without one
// still cannot hang. A file upload can be larger than an invite read, so the belt
// is looser than discordinvite's.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// ListTextChannels lists guildID's text channels (type 0), sorted by position then
// name, using token (a Discord Bot token). An empty token fails fast (no dial). A
// ctx deadline bounds the call. log may be nil.
func ListTextChannels(ctx context.Context, token, guildID string, log *slog.Logger) ([]Channel, error) {
	return listTextChannels(ctx, token, guildID, "", log)
}

// listTextChannels is ListTextChannels with a base-URL seam: "" means the live
// Discord API, tests point it at a fake HTTP server.
func listTextChannels(ctx context.Context, token, guildID, baseURL string, log *slog.Logger) ([]Channel, error) {
	if token == "" {
		return nil, errors.New("discordshare: empty bot token")
	}
	if guildID == "" {
		return nil, errors.New("discordshare: empty guild id")
	}
	if log == nil {
		log = slog.Default()
	}
	if baseURL == "" {
		baseURL = liveAPIBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/guilds/"+url.PathEscape(guildID)+"/channels", nil)
	if err != nil {
		return nil, fmt.Errorf("discordshare: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discordshare: GET /guilds/channels: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoAccess
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Op: "list channels", Status: resp.StatusCode}
	}

	var channels []struct {
		ID       string `json:"id"`
		Type     int    `json:"type"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return nil, fmt.Errorf("discordshare: decode channels: %w", err)
	}

	type positioned struct {
		ch  Channel
		pos int
	}
	kept := make([]positioned, 0, len(channels))
	for _, c := range channels {
		if c.Type != guildTextType {
			continue
		}
		kept = append(kept, positioned{ch: Channel{ID: c.ID, Name: c.Name}, pos: c.Position})
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].pos != kept[j].pos {
			return kept[i].pos < kept[j].pos
		}
		return kept[i].ch.Name < kept[j].ch.Name
	})
	out := make([]Channel, len(kept))
	for i, k := range kept {
		out[i] = k.ch
	}

	log.Debug("listed Discord text channels", "guild", guildID, "textChannels", len(out))
	return out, nil
}

// PostFile uploads data as a single message attachment to channelID with caption
// as the message content, using token. filename + contentType describe the
// attachment (files[0] in Discord's multipart form). An empty token fails fast. A
// non-2xx response is an *APIError (413 oversize, 4xx/5xx) so the RPC maps it to a
// readable CodeUnavailable. log may be nil.
func PostFile(ctx context.Context, token, channelID, caption, filename, contentType string, data []byte, log *slog.Logger) error {
	return postFile(ctx, token, channelID, caption, filename, contentType, data, "", log)
}

// postFile is PostFile with a base-URL seam.
func postFile(ctx context.Context, token, channelID, caption, filename, contentType string, data []byte, baseURL string, log *slog.Logger) error {
	if token == "" {
		return errors.New("discordshare: empty bot token")
	}
	if channelID == "" {
		return errors.New("discordshare: empty channel id")
	}
	if log == nil {
		log = slog.Default()
	}
	if baseURL == "" {
		baseURL = liveAPIBaseURL
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// payload_json carries the message content + the attachment descriptor Discord
	// pairs with files[0] by index. allowed_mentions suppresses EVERY mention: the
	// caption is a raw transcript/LLM excerpt and this is the codebase's first public
	// post — an "@everyone" (or a user/role mention) in an excerpt must NOT ping the
	// guild. An empty parse list disables all four mention types (Discord's docs).
	payload, err := json.Marshal(map[string]any{
		"content": caption,
		"attachments": []map[string]any{
			{"id": 0, "filename": filename},
		},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err != nil {
		return fmt.Errorf("discordshare: marshal payload: %w", err)
	}
	if err := mw.WriteField("payload_json", string(payload)); err != nil {
		return fmt.Errorf("discordshare: write payload field: %w", err)
	}

	// files[0] is the attachment part; set an explicit Content-Type so Discord
	// stores the clip as audio/wav rather than octet-stream (the multipart helper's
	// CreateFormFile hard-codes octet-stream, so build the part header by hand).
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[0]"; filename=%q`, filename))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("discordshare: create file part: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("discordshare: write file part: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("discordshare: close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/channels/"+url.PathEscape(channelID)+"/messages", &body)
	if err != nil {
		return fmt.Errorf("discordshare: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("discordshare: POST /channels/messages: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Op: "post file", Status: resp.StatusCode}
	}
	log.Debug("posted Discord highlight file", "channel", channelID, "bytes", len(data))
	return nil
}
