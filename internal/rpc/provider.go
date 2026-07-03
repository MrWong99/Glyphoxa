package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

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
	UpsertProviderConfigs(ctx context.Context, configs []storage.NewProviderConfig) ([]storage.ProviderConfig, error)
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	SaveDiscordBotToken(ctx context.Context, tenantID uuid.UUID, ciphertext []byte, last4 string) (storage.DeploymentConfig, error)
	SaveDiscordChannels(ctx context.Context, tenantID uuid.UUID, guildID, voiceChannelID string) (storage.DeploymentConfig, error)
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
}

var _ managementv1connect.ProviderServiceHandler = (*ProviderServer)(nil)

// NewProviderServer wraps a providerStore + cipher in a ProviderServer. A nil
// cipher disables secret saves (CodeFailedPrecondition) while keeping reads
// available, matching the keyless-degradation posture of the web tier.
func NewProviderServer(store providerStore, cipher *crypto.Cipher, log *slog.Logger) *ProviderServer {
	if log == nil {
		log = slog.Default()
	}
	return &ProviderServer{store: store, cipher: cipher, log: log}
}

// SetHealthInvalidator wires the health-cache buster called after a successful
// credential save (#150). Called once at boot, before the server serves, so no
// lock is needed.
func (s *ProviderServer) SetHealthInvalidator(fn func(tenantID uuid.UUID)) {
	s.invalidateHealth = fn
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

	return connect.NewResponse(&managementv1.ListProviderConfigsResponse{
		Credentials:    creds,
		GuildId:        dep.GuildID,
		VoiceChannelId: dep.VoiceChannelID,
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
	if s.cipher == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
	}

	slot, ok := slotFor(req.Msg.GetProvider())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown provider %q", req.Msg.GetProvider()))
	}
	secret := req.Msg.GetSecret()
	if secret == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is required"))
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

	return connect.NewResponse(&managementv1.SaveDiscordSettingsResponse{
		Credential:     discordCredential(dep),
		GuildId:        dep.GuildID,
		VoiceChannelId: dep.VoiceChannelID,
	}), nil
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
