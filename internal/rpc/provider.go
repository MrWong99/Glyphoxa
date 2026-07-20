package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordinvite"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// inviteCodePattern bounds an accepted Discord invite code: 2–64 chars of
// letters, digits, and hyphens (vanity codes included). The SPA already extracted
// the bare code from the pasted URL; this rejects anything that could not be one
// before it reaches the REST call (which is also path-escaped, ADR-0047).
var inviteCodePattern = regexp.MustCompile(`^[A-Za-z0-9-]{2,64}$`)

// envPlaceholderLast4 is the credentials_last4 the seed writes for a
// provider_config whose real key lives in ENV/the keyring, not the DB (ADR-0039;
// matches wirenpc's credPlaceholderLast4). A slot still holding it has no real
// saved key, so the Configuration screen renders it as "Key needed".
const envPlaceholderLast4 = "env"

// providerStore is the narrow Configuration surface ProviderServer needs;
// *storage.Store satisfies it. Handlers depend on this interface (not the
// concrete store) so they unit-test keyless with a fake.
type providerStore interface {
	ListProviderConfigs(ctx context.Context, tenantID uuid.UUID) ([]storage.ProviderConfig, error)
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
	UpsertProviderConfigs(ctx context.Context, configs []storage.NewProviderConfig) ([]storage.ProviderConfig, error)
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	SaveDiscordBotToken(ctx context.Context, tenantID uuid.UUID, ciphertext []byte, last4 string) (storage.DeploymentConfig, error)
	SaveDiscordChannels(ctx context.Context, tenantID uuid.UUID, guildID, voiceChannelID string) (storage.DeploymentConfig, error)
	// GetTenantSpendCaps / SetTenantSpendCaps back the per-Tenant spend caps
	// Configuration surface (#130, ADR-0046).
	GetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID) (storage.SpendCaps, error)
	SetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID, caps storage.SpendCaps) error
}

// byokSlot is one Bring-Your-Own-Key provider the Configuration screen saves. A
// provider can back several Components — ElevenLabs powers STT + TTS from one
// key (ADR-0004) — so saving it upserts every listed Component; the first is the
// display Component on the wire.
type byokSlot struct {
	provider   string
	components []storage.Component
}

// byokSlots is the MVP provider matrix (ADR-0039): Groq (LLM) + ElevenLabs
// (STT + TTS). The Discord bot token is NOT here — it is the deployment-shared
// Bot (CONTEXT.md), stored in deployment_config, not a per-Component
// provider_config.
var byokSlots = []byokSlot{
	{provider: "groq", components: []storage.Component{storage.ComponentLLM}},
	{provider: "elevenlabs", components: []storage.Component{storage.ComponentTTS, storage.ComponentSTT}},
	// Gemini backs the image Component (#311, ADR-0004 amendment): a distinct
	// BYOK slot + health check, even though Gemini already appears in the LLM
	// matrix — the image key is managed on its own Configuration row.
	{provider: "gemini", components: []storage.Component{storage.ComponentImage}},
}

func slotFor(provider string) (byokSlot, bool) {
	for _, s := range byokSlots {
		if s.provider == provider {
			return s, true
		}
	}
	return byokSlot{}, false
}

// ProviderServer implements the Connect ProviderService over a providerStore and
// the app cipher (ADR-0004 / ADR-0039). It enforces the write-only contract: a
// saved secret's plaintext is sealed on the way in and never read back out — RPC
// responses carry only last4 + metadata.
type ProviderServer struct {
	store  providerStore
	cipher *crypto.Cipher // nil when $GLYPHOXA_SECRET is unset: reads still work, saves fail loudly
	log    *slog.Logger

	// invalidateHealth busts the tenant's provider-health cache after a
	// credential save (#150), so a cached Degraded badge cannot outlive the
	// fixed key for up to a TTL. nil (not wired) skips.
	invalidateHealth func(tenantID uuid.UUID)

	// refreshPresence reconciles the standing Discord presence for ONE Tenant after
	// its Discord settings change (#489), so a newly-saved Bot token / Guild
	// registers the slash-command surface without a restart and without touching
	// any other Tenant's client. Fired in a goroutine (the per-tenant ensure does
	// network I/O). nil (web-only, or not wired) skips.
	refreshPresence func(tenantID uuid.UUID)

	// integrationStatus reports a Tenant's standing Discord client health for the
	// Configuration read (#489): state ∈ {"ok","waiting","failed"} plus a detail.
	// nil (web-only, no standing presence) leaves both empty.
	integrationStatus func(tenantID uuid.UUID) (state, detail string)

	// discordAppID is the Discord application (client) id backing operator login
	// (ADR-0016), surfaced on ListProviderConfigs so the SPA composes the
	// bot-authorization URL (#110). Non-secret; empty when DISCORD_OAUTH_CLIENT_ID
	// is unset, which the screen renders as a disabled action.
	discordAppID string

	// resolveInvite resolves a Discord invite code to its guild + voice channels
	// using the decrypted Bot token (#105). It is a seam: NewProviderServer
	// defaults it to the live discordinvite.Resolve, and unit tests override it
	// with a fake so ResolveGuildInvite never touches the network.
	resolveInvite func(ctx context.Context, token, code string) (discordinvite.Resolved, error)
}

var _ managementv1connect.ProviderServiceHandler = (*ProviderServer)(nil)

// NewProviderServer wraps a providerStore + cipher in a ProviderServer. A nil
// cipher disables secret saves (CodeFailedPrecondition) while keeping reads
// available, matching the keyless-degradation posture of the web tier.
func NewProviderServer(store providerStore, cipher *crypto.Cipher, log *slog.Logger) *ProviderServer {
	if log == nil {
		log = slog.Default()
	}
	s := &ProviderServer{store: store, cipher: cipher, log: log}
	s.resolveInvite = func(ctx context.Context, token, code string) (discordinvite.Resolved, error) {
		return discordinvite.Resolve(ctx, token, code, log)
	}
	return s
}

// SetHealthInvalidator wires the health-cache buster called after a successful
// credential save (#150). Called once at boot, before the server serves, so no
// lock is needed.
func (s *ProviderServer) SetHealthInvalidator(fn func(tenantID uuid.UUID)) {
	s.invalidateHealth = fn
}

// SetPresenceRefresher wires the per-tenant standing-presence reconciler fired
// after a successful SaveDiscordSettings (#489), mirroring SetHealthInvalidator.
// Called once at boot, before the server serves, so no lock is needed. fn is
// invoked in a goroutine because the per-tenant ensure does network I/O
// (OpenGateway) that must not block the RPC response.
func (s *ProviderServer) SetPresenceRefresher(fn func(tenantID uuid.UUID)) {
	s.refreshPresence = fn
}

// SetIntegrationStatusSource wires the per-tenant Discord integration health read
// surfaced on ListProviderConfigs (#489), mirroring SetPresenceRefresher. Called
// once at boot; nil (web-only) leaves the state empty.
func (s *ProviderServer) SetIntegrationStatusSource(fn func(tenantID uuid.UUID) (state, detail string)) {
	s.integrationStatus = fn
}

// SetDiscordApplicationID wires the Discord application (client) id ListProviderConfigs
// echoes so the SPA composes the bot-authorization URL (#110), mirroring
// SetHealthInvalidator/SetPresenceRefresher. Called once at boot, before the
// server serves, so no lock is needed. The empty string (DISCORD_OAUTH_CLIENT_ID
// unset) is the missing-app-id fallback the screen renders as disabled.
func (s *ProviderServer) SetDiscordApplicationID(id string) {
	s.discordAppID = id
}

// Handler builds the Connect HTTP handler for ProviderService and returns its
// mount path + handler, mirroring (*CampaignServer).Handler.
func (s *ProviderServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewProviderServiceHandler(s, opts...)
}

// tenant resolves the operator's tenant the auth interceptor stack put in the
// context (ADR-0039 thin pass-through). Behind the stack this is always present;
// a missing tenant is treated as unauthenticated.
func (s *ProviderServer) tenant(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.TenantID(ctx)
	if !ok {
		return uuid.Nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}
	return id, nil
}

// ListProviderConfigs returns the three write-only credential slots (Discord,
// Groq, ElevenLabs) plus the non-secret Discord IDs. No secret value is read.
func (s *ProviderServer) ListProviderConfigs(
	ctx context.Context,
	_ *connect.Request[managementv1.ListProviderConfigsRequest],
) (*connect.Response[managementv1.ListProviderConfigsResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	configs, err := s.store.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		s.log.Error("ListProviderConfigs: list provider_config failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// Index by provider, keeping the most-recently-updated row (ElevenLabs has
	// two rows — stt + tts — sharing one key; either represents the slot).
	byProvider := make(map[string]storage.ProviderConfig, len(configs))
	for _, c := range configs {
		if cur, ok := byProvider[c.Provider]; !ok || c.UpdatedAt.After(cur.UpdatedAt) {
			byProvider[c.Provider] = c
		}
	}

	dep, err := s.store.GetDeploymentConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.log.Error("ListProviderConfigs: get deployment_config failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// ErrNotFound → zero DeploymentConfig (all empty): the unsaved, key-needed state.

	creds := []*managementv1.ProviderCredential{discordCredential(dep)}
	for _, slot := range byokSlots {
		creds = append(creds, providerCredential(string(slot.components[0]), slot.provider, byProvider[slot.provider]))
	}

	// Per-tenant Discord integration health (#489): the standing client's state
	// for THIS Tenant. Empty in web-only mode (no source wired).
	var integrationState, integrationDetail string
	if s.integrationStatus != nil {
		integrationState, integrationDetail = s.integrationStatus(tenantID)
	}

	return connect.NewResponse(&managementv1.ListProviderConfigsResponse{
		Credentials:          creds,
		GuildId:              dep.GuildID,
		VoiceChannelId:       dep.VoiceChannelID,
		DiscordApplicationId: s.discordAppID,
		IntegrationState:     integrationState,
		IntegrationDetail:    integrationDetail,
	}), nil
}

// SaveProviderConfig seals a BYOK provider key and upserts every Component the
// provider backs, returning only the saved key's masked metadata.
func (s *ProviderServer) SaveProviderConfig(
	ctx context.Context,
	req *connect.Request[managementv1.SaveProviderConfigRequest],
) (*connect.Response[managementv1.SaveProviderConfigResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	slot, ok := slotFor(req.Msg.GetProvider())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown provider %q", req.Msg.GetProvider()))
	}
	secret := req.Msg.GetSecret()
	if secret == "" {
		// No secret on the wire = a model-only update for an already-saved key
		// (#227): free-text model entry must not force the operator to re-paste
		// the secret. Needs no cipher — nothing is sealed.
		return s.saveModelOnly(ctx, tenantID, slot, req.Msg.GetModel())
	}
	if s.cipher == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
	}

	sealed, last4, err := s.seal(secret)
	if err != nil {
		s.log.Error("SaveProviderConfig: seal failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	model := req.Msg.GetModel()
	batch := make([]storage.NewProviderConfig, 0, len(slot.components))
	for _, comp := range slot.components {
		batch = append(batch, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             comp,
			Provider:              slot.provider,
			Model:                 model,
			CredentialsCiphertext: sealed,
			CredentialsLast4:      last4,
		})
	}
	saved, err := s.store.UpsertProviderConfigs(ctx, batch)
	if err != nil {
		s.log.Error("SaveProviderConfig: upsert failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// The key changed: the cached health verdict (possibly Degraded from the
	// old key) is stale — bust it so the next health call probes fresh (#150).
	if s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}

	return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
		Credential: providerCredential(string(slot.components[0]), slot.provider, saved[0]),
	}), nil
}

// saveModelOnly handles a SaveProviderConfig carrying no secret (#227): a
// model-only update for a provider whose key is already stored. Each of the
// slot's component rows is re-upserted with its stored ciphertext/last4
// verbatim and the new model — the secret stays write-only and untouched
// (ADR-0004). With no stored row the request is rejected exactly like the
// pre-#227 empty-secret save: a model alone cannot create a credential slot.
// An empty model together with the empty secret is a read-back no-op — such a
// request only reaches the wire by accident (mirrors #142's posture on empty
// Discord IDs). The health cache is deliberately NOT busted: the key did not
// change, so the cached verdict is still valid.
func (s *ProviderServer) saveModelOnly(
	ctx context.Context,
	tenantID uuid.UUID,
	slot byokSlot,
	model string,
) (*connect.Response[managementv1.SaveProviderConfigResponse], error) {
	existing := make([]storage.ProviderConfig, 0, len(slot.components))
	for _, comp := range slot.components {
		cfg, err := s.store.GetProviderConfigByComponent(ctx, tenantID, comp)
		if errors.Is(err, storage.ErrNotFound) || (err == nil && cfg.Provider != slot.provider) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is required"))
		}
		if err != nil {
			s.log.Error("SaveProviderConfig: read provider_config for model-only save failed", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		existing = append(existing, cfg)
	}

	if model == "" {
		return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
			Credential: providerCredential(string(slot.components[0]), slot.provider, existing[0]),
		}), nil
	}

	batch := make([]storage.NewProviderConfig, 0, len(existing))
	for _, cfg := range existing {
		batch = append(batch, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             cfg.Component,
			Provider:              slot.provider,
			Model:                 model,
			CredentialsCiphertext: cfg.CredentialsCiphertext,
			CredentialsLast4:      cfg.CredentialsLast4,
		})
	}
	saved, err := s.store.UpsertProviderConfigs(ctx, batch)
	if err != nil {
		s.log.Error("SaveProviderConfig: model-only upsert failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
		Credential: providerCredential(string(slot.components[0]), slot.provider, saved[0]),
	}), nil
}

// SaveDiscordSettings stores the Discord bot token (when present) and the
// non-secret Guild / Voice channel IDs (when present). Every field has wire
// presence: an omitted bot_token leaves the stored token untouched, and omitted
// IDs leave the stored IDs untouched (#142) — so the token Save and the IDs
// Save never clobber each other.
func (s *ProviderServer) SaveDiscordSettings(
	ctx context.Context,
	req *connect.Request[managementv1.SaveDiscordSettingsRequest],
) (*connect.Response[managementv1.SaveDiscordSettingsResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	// Validate the IDs before any write so a rejected request mutates nothing.
	// Present-but-empty is REJECTED, not treated as a clear (mirrors bot_token's
	// empty check): an empty ID only reaches the wire by accident — e.g. the form
	// saving before the config load resolves — and clearing is unsupported (#142).
	hasIDs := req.Msg.GuildId != nil || req.Msg.VoiceChannelId != nil
	if hasIDs && (req.Msg.GetGuildId() == "" || req.Msg.GetVoiceChannelId() == "") {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("guild_id and voice_channel_id must both be non-empty when provided"))
	}

	var dep storage.DeploymentConfig

	// Bot token first (when the client sent one), so the IDs upsert below returns
	// the row with the freshly-saved token last4.
	if req.Msg.BotToken != nil {
		if s.cipher == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
		}
		token := req.Msg.GetBotToken()
		if token == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("bot_token must not be empty when provided"))
		}
		sealed, last4, err := s.seal(token)
		if err != nil {
			s.log.Error("SaveDiscordSettings: seal failed", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		dep, err = s.store.SaveDiscordBotToken(ctx, tenantID, sealed, last4)
		if err != nil {
			s.log.Error("SaveDiscordSettings: save token failed", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}

	// IDs only when present on the wire (mirrors the token's presence handling):
	// a token-only save must never touch the stored IDs (#142).
	if hasIDs {
		dep, err = s.store.SaveDiscordChannels(ctx, tenantID, req.Msg.GetGuildId(), req.Msg.GetVoiceChannelId())
		if errors.Is(err, storage.ErrGuildTaken) {
			// First-registrar-wins guild binding (#483; the full guild-permission
			// proof — verifying the saver actually administers the guild — is #504).
			// A deliberate precondition refusal, never a silent rebind: the old
			// newest-wins read let a second Tenant hijack the first's guild and with
			// it the victim's voice-channel member reads + command routing.
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("this Discord server is already linked by another tenant; it must unlink there first"))
		}
		if err != nil {
			s.log.Error("SaveDiscordSettings: save channels failed", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}

	// Discord config changed (token and/or IDs): bust the cached health
	// verdict so the next health call probes with the new state (#150).
	if s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}

	// Reconcile THIS Tenant's standing presence out-of-band so the new token /
	// Guild registers the slash-command surface without a restart and without
	// touching any other Tenant's client (#489). Only after a successful save —
	// the error returns above skip it. In a goroutine because the ensure opens a
	// gateway (network I/O) and must not block this response.
	if s.refreshPresence != nil {
		go s.refreshPresence(tenantID)
	}

	return connect.NewResponse(&managementv1.SaveDiscordSettingsResponse{
		Credential:     discordCredential(dep),
		GuildId:        dep.GuildID,
		VoiceChannelId: dep.VoiceChannelID,
	}), nil
}

// ResolveGuildInvite resolves a pasted Discord invite code to its Guild and that
// Guild's voice channels, using the decrypted saved Bot token server-side (#105,
// ADR-0047). It is a no-side-effects read: the resolver makes only Discord REST
// GETs, and no state is written. The code is validated + path-escaped before the
// call; ErrNotFound → NotFound, ErrNoAccess → FailedPrecondition ("not a member"),
// and a missing saved token → FailedPrecondition ("save the token first").
func (s *ProviderServer) ResolveGuildInvite(
	ctx context.Context,
	req *connect.Request[managementv1.ResolveGuildInviteRequest],
) (*connect.Response[managementv1.ResolveGuildInviteResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	code := req.Msg.GetInviteCode()
	if !inviteCodePattern.MatchString(code) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid invite code"))
	}

	token, err := s.resolveBotToken(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		// No saved Bot token (nor the ENV placeholder): the resolver cannot
		// authenticate. Same code as "not a member" — the screen renders whichever
		// message it gets, so they must differ (ADR-0047).
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("save the Discord bot token first"))
	}

	resolved, err := s.resolveInvite(ctx, token, code)
	if err != nil {
		switch {
		case errors.Is(err, discordinvite.ErrNotFound):
			return nil, connect.NewError(connect.CodeNotFound, errors.New("invalid or expired invite"))
		case errors.Is(err, discordinvite.ErrNoAccess):
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("the Bot is not a member of that server"))
		default:
			// A transport failure wraps *url.Error, whose text embeds the request
			// URL — and thus the invite code, a join capability (ADR-0047). Scrub
			// the code before logging; the op + status text still diagnose.
			s.log.Error("ResolveGuildInvite: resolve failed", "err", redactInviteCode(err, code))
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}

	channels := make([]*managementv1.VoiceChannel, 0, len(resolved.VoiceChannels))
	for _, vc := range resolved.VoiceChannels {
		channels = append(channels, &managementv1.VoiceChannel{Id: vc.ID, Name: vc.Name})
	}
	return connect.NewResponse(&managementv1.ResolveGuildInviteResponse{
		GuildId:       resolved.Guild.ID,
		GuildName:     resolved.Guild.Name,
		VoiceChannels: channels,
	}), nil
}

// redactInviteCode strips the invite code from an internal-error string bound for
// the log. Transport failures wrap *url.Error whose text carries the request URL
// (code included); the code is a join capability that must not land in logs
// (ADR-0047). The op and status text survive so a failure is still diagnosable.
// The code is guaranteed ^[A-Za-z0-9-]{2,64}$, so a literal replace is safe.
func redactInviteCode(err error, code string) string {
	if code == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), code, "[invite-code]")
}

// GetSpendCaps returns the operator's two per-Tenant spend caps (#130, ADR-0046),
// each absent when unset. A missing tenant row is the no-caps default (both unset),
// not an error. A read (NO_SIDE_EFFECTS).
func (s *ProviderServer) GetSpendCaps(
	ctx context.Context,
	_ *connect.Request[managementv1.GetSpendCapsRequest],
) (*connect.Response[managementv1.GetSpendCapsResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	caps, err := s.store.GetTenantSpendCaps(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.log.Error("GetSpendCaps: store read failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.GetSpendCapsResponse{Caps: toProtoSpendCaps(caps)}), nil
}

// SetSpendCaps stores the operator's two per-Tenant spend caps (#130). An omitted
// field clears that cap; a negative value is InvalidArgument, and both-set requires
// hard >= soft (InvalidArgument). Caps snapshot at the NEXT Voice Session start.
func (s *ProviderServer) SetSpendCaps(
	ctx context.Context,
	req *connect.Request[managementv1.SetSpendCapsRequest],
) (*connect.Response[managementv1.SetSpendCapsResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	var caps storage.SpendCaps
	if v := req.Msg.SoftUsd; v != nil {
		if *v < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("soft cap must not be negative"))
		}
		soft := *v
		caps.SoftUSD = &soft
	}
	if v := req.Msg.HardUsd; v != nil {
		if *v < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hard cap must not be negative"))
		}
		hard := *v
		caps.HardUSD = &hard
	}
	if caps.SoftUSD != nil && caps.HardUSD != nil && *caps.HardUSD < *caps.SoftUSD {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hard cap must be greater than or equal to the soft cap"))
	}

	if err := s.store.SetTenantSpendCaps(ctx, tenantID, caps); err != nil {
		s.log.Error("SetSpendCaps: store write failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SetSpendCapsResponse{Caps: toProtoSpendCaps(caps)}), nil
}

// toProtoSpendCaps maps storage caps onto the wire view: a nil pointer stays absent
// (no presence), so the screen distinguishes "unset" from "0".
func toProtoSpendCaps(c storage.SpendCaps) *managementv1.SpendCaps {
	return &managementv1.SpendCaps{SoftUsd: c.SoftUSD, HardUsd: c.HardUSD}
}

// resolveBotToken resolves the deployment Bot token for ResolveGuildInvite,
// mirroring VoiceServer.resolveDiscordToken: no row / ENV placeholder -> "" (the
// caller maps that to a FailedPrecondition), a real saved token -> decrypted
// plaintext, a saved token with no cipher -> FailedPrecondition.
func (s *ProviderServer) resolveBotToken(ctx context.Context, tenantID uuid.UUID) (string, error) {
	dep, err := s.store.GetDeploymentConfig(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		s.log.Error("resolveBotToken: store read failed", "err", err)
		return "", connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return s.openKey(dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext)
}

// openKey applies the hybrid decision to one (last4, ciphertext) pair, mirroring
// VoiceServer.openKey: an unsaved/placeholder last4 -> "", a real key with no
// cipher -> FailedPrecondition, otherwise the decrypted plaintext.
//
// Entitlement posture (ADR-0054 seam (a), ADR-0055 — the former PHASE-B GAP,
// verified closed-by-construction): the ONLY caller is resolveBotToken, i.e.
// the Discord Bot token, which is deployment infrastructure — not a Platform
// provider key — and stays outside the entitlement. No provider-key resolution
// flows through here; if one is ever added it must go through
// llmbuild.ResolveKeyGated instead (see VoiceServer.resolveComponentKey).
func (s *ProviderServer) openKey(last4 string, ciphertext []byte) (string, error) {
	if !isSaved(last4) {
		return "", nil
	}
	if s.cipher == nil {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
	}
	plaintext, err := s.cipher.Open(ciphertext)
	if err != nil {
		s.log.Error("openKey: decrypt failed", "err", err)
		return "", connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return string(plaintext), nil
}

// seal encrypts a plaintext secret and returns the ciphertext + its last4. The
// caller guards s.cipher != nil before calling.
func (s *ProviderServer) seal(secret string) (ciphertext []byte, last4 string, err error) {
	sealed, err := s.cipher.Seal([]byte(secret))
	if err != nil {
		return nil, "", err
	}
	return sealed, crypto.Last4(secret), nil
}

// providerCredential maps a stored provider_config onto its write-only wire
// view. A zero-value config (no row) or one still holding the ENV placeholder is
// reported as never-saved.
func providerCredential(component, provider string, c storage.ProviderConfig) *managementv1.ProviderCredential {
	saved := isSaved(c.CredentialsLast4)
	cred := &managementv1.ProviderCredential{
		Component:  component,
		Provider:   provider,
		EverSaved:  saved,
		ShowMasked: saved,
		Model:      c.Model,
	}
	if saved {
		cred.Last4 = c.CredentialsLast4
		cred.UpdatedAt = timestamppb.New(c.UpdatedAt)
	}
	return cred
}

// discordCredential maps the deployment Bot token onto its write-only wire view.
func discordCredential(d storage.DeploymentConfig) *managementv1.ProviderCredential {
	saved := isSaved(d.DiscordBotTokenLast4)
	cred := &managementv1.ProviderCredential{
		Component:  "discord",
		Provider:   "discord",
		EverSaved:  saved,
		ShowMasked: saved,
	}
	if saved {
		cred.Last4 = d.DiscordBotTokenLast4
		cred.UpdatedAt = timestamppb.New(d.UpdatedAt)
	}
	return cred
}

// isSaved reports whether a last4 marks a real saved key: non-empty and not the
// seed's ENV placeholder.
func isSaved(last4 string) bool {
	return last4 != "" && last4 != envPlaceholderLast4
}
