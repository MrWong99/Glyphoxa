package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordguild"
	"github.com/MrWong99/Glyphoxa/internal/discordinvite"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// inviteCodePattern bounds an accepted Discord invite code: 2–64 chars of
// letters, digits, and hyphens (vanity codes included). The SPA already extracted
// the bare code from the pasted URL; this rejects anything that could not be one
// before it reaches the REST call (which is also path-escaped, ADR-0047).
var inviteCodePattern = regexp.MustCompile(`^[A-Za-z0-9-]{2,64}$`)

// envPlaceholderLast4 is the credentials_last4 the seed writes for a
// provider_config whose real key lives in ENV/the keyring, not the DB (ADR-0039;
// matches wirenpc's credPlaceholderLast4). A slot still holding it has no real
// saved key, so the Configuration screen renders it as "Key needed". It is
// [storage.KeylessLast4] under its local name, not a second spelling.
const envPlaceholderLast4 = storage.KeylessLast4

// providerStore is the narrow Configuration surface ProviderServer needs;
// *storage.Store satisfies it. Handlers depend on this interface (not the
// concrete store) so they unit-test keyless with a fake.
type providerStore interface {
	ListProviderConfigs(ctx context.Context, tenantID uuid.UUID) ([]storage.ProviderConfig, error)
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
	UpsertProviderConfigs(ctx context.Context, configs []storage.NewProviderConfig) ([]storage.ProviderConfig, error)
	// SaveChatLLMChoice / DeleteChatLLMConfig write the Tenant's single
	// Planning chat model choice (ADR-0062 amendment 2026-11-22).
	SaveChatLLMChoice(ctx context.Context, c storage.ChatLLMChoice) (storage.ProviderConfig, error)
	DeleteChatLLMConfig(ctx context.Context, tenantID uuid.UUID) error
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	SaveDiscordBotToken(ctx context.Context, tenantID uuid.UUID, ciphertext []byte, last4 string) (storage.DeploymentConfig, error)
	SaveDiscordChannels(ctx context.Context, tenantID uuid.UUID, guildID, voiceChannelID string) (storage.DeploymentConfig, error)
	SaveDiscordGuild(ctx context.Context, tenantID uuid.UUID, guildID string) (storage.DeploymentConfig, error)
	SaveDefaultVoiceChannel(ctx context.Context, tenantID uuid.UUID, voiceChannelID string) (storage.DeploymentConfig, error)
	// ReleaseDiscordGuild frees the Tenant's bound guild via an atomic
	// compare-and-clear (#504): the echoed guild_id must match the binding.
	ReleaseDiscordGuild(ctx context.Context, tenantID uuid.UUID, guildID string) (storage.DeploymentConfig, error)
	// SaveDiscordGuildName stores the linked server's display name, read by the
	// guild-admin proof at link time; a no-op unless guildID is still linked.
	SaveDiscordGuildName(ctx context.Context, tenantID uuid.UUID, guildID, name string) (bool, error)
	// InsertKeylessProviderConfig writes a "ride the Platform Key" row only
	// when the slot has none, returning the existing row untouched otherwise
	// (#712 item 3: activating can never clear a key saved concurrently).
	InsertKeylessProviderConfig(ctx context.Context, c storage.NewProviderConfig) (storage.ProviderConfig, bool, error)
	// DeleteTenantLLMConfigs removes the Tenant's voice LLM rows of the given
	// providers (ADR-0004 amendment 2026-11-26).
	DeleteTenantLLMConfigs(ctx context.Context, tenantID uuid.UUID, providers []string) (int64, error)
	// GetTenantSpendCaps / SetTenantSpendCaps back the per-Tenant spend caps
	// Configuration surface (#130, ADR-0046).
	GetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID) (storage.SpendCaps, error)
	SetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID, caps storage.SpendCaps) error
}

// byokSlot is one Bring-Your-Own-Key provider the Configuration screen saves. A
// provider can back several Components — ElevenLabs powers STT + TTS from one
// key (ADR-0004) — so saving it upserts every listed Component; the first is the
// display Component on the wire. A save names a slot by its provider. The
// Planning chat is NOT a slot: it is a choice over the LLM providers with its
// own handler (provider_chat.go, ADR-0062 amendment 2026-11-22).
type byokSlot struct {
	provider   string
	components []storage.Component
	// envKey is the deployment env var the provider adapter falls back to when
	// the Tenant saved no key (ADR-0039 hybrid policy) — the Platform Key whose
	// presence makes the slot "provided by the platform" (W9).
	envKey string
}

// byokSlots is the MVP provider matrix (ADR-0039): Groq (LLM) + ElevenLabs
// (STT + TTS). The Discord bot token is NOT here — it is the deployment-shared
// Bot (CONTEXT.md), stored in deployment_config, not a per-Component
// provider_config.
var byokSlots = []byokSlot{
	{provider: "groq", components: []storage.Component{storage.ComponentLLM}, envKey: groq.APIKeyEnv},
	// Anthropic is the second voice-LLM slot (#702): its own (llm, anthropic)
	// row beside Groq's. A Tenant runs whichever LLM row it saved most recently
	// (storage.GetProviderConfigByComponent), so the two slots are a choice, not
	// a pair: saving one — or activating it — puts the Tenant on it.
	{provider: anthropic.ProviderID, components: []storage.Component{storage.ComponentLLM}, envKey: anthropic.APIKeyEnv},
	{provider: "elevenlabs", components: []storage.Component{storage.ComponentTTS, storage.ComponentSTT}, envKey: elevenlabs.APIKeyEnv},
	// Gemini backs the image Component (#311, ADR-0004 amendment): a distinct
	// BYOK slot + health check, even though Gemini already appears in the LLM
	// matrix — the image key is managed on its own Configuration row.
	{provider: "gemini", components: []storage.Component{storage.ComponentImage}, envKey: gemini.APIKeyEnv},
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

	// llmDefault is the deployment-default LLM (#702); see SetLLMDefault.
	llmDefault llmbuild.DeploymentDefault

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

	// checkGuildAdmin proves the saver administers the guild being bound (#504,
	// ADR-0058): guild owner or a role with ADMINISTRATOR/MANAGE_GUILD. On
	// success it returns the guild's display name, read by the same guild
	// fetch, which is stored with the link. It is a seam mirroring
	// resolveInvite: NewProviderServer defaults it to the live
	// discordguild.CheckAdminGuild, and unit tests override it so
	// SaveDiscordSettings never touches the network (ADR-0033 keyless suite).
	checkGuildAdmin func(ctx context.Context, token, guildID, userID string) (string, error)

	// voiceChannelInGuild reports whether channelID is one of guildID's voice
	// channels, read with a Bot token, so a Default Voice Channel save can prove
	// the channel belongs to the guild (R9). A seam like checkGuildAdmin:
	// defaults to a live discordshare.ListVoiceChannels; unit tests fake it.
	voiceChannelInGuild func(ctx context.Context, token, guildID, channelID string) (bool, error)

	// envBotToken is the deployment's central DISCORD_BOT_TOKEN (SetEnvBotToken),
	// the last rung of the check-token ladder (ADR-0058): request plaintext >
	// stored BYOK token > this. Used ONLY to authenticate read-only Discord REST
	// calls — the guild-admin proof (#504) and invite resolution — and never
	// persisted or returned (ADR-0004 posture).
	envBotToken string

	// keyEnt is the platform-key entitlement (ADR-0054 seam (a), ADR-0055) the
	// call-time key resolution consults before riding the env fallback; the
	// Configuration read consults the SAME gate (via llmbuild.ResolveKeyGated)
	// to report a slot as provided by the platform (W9). nil grants everything —
	// the allowlist/self-host posture, as on VoiceServer.
	keyEnt llmbuild.PlatformKeyEntitlement
	// lookupEnv reads a deployment env var (os.Getenv in prod); a seam so tests
	// pin the "platform key present" half of provided_by_platform.
	lookupEnv func(string) string

	// plans reads the Tenant's active plan for GetUsageSummary; nil (not
	// wired) reports no plan. allowance is the monthly allowance read the
	// session and off-session gates use (ADR-0055 gate (b)); nil — allowlist
	// mode, where no allowance gates anything — reports none. See
	// SetUsageSources.
	plans     activePlanReader
	allowance allowanceStateReader
	// now is the clock behind GetUsageSummary's resets_at; a test seam.
	now func() time.Time
}

// activePlanReader is the plan read behind GetUsageSummary; *storage.Store
// satisfies it.
type activePlanReader interface {
	TenantActivePlan(ctx context.Context, tenantID uuid.UUID) (storage.Plan, error)
}

// allowanceStateReader is the monthly allowance read; spend.PlanAllowance
// satisfies it, the same value the session and off-session gates hold.
type allowanceStateReader interface {
	AllowanceState(ctx context.Context, tenantID uuid.UUID) (spend.AllowanceState, error)
}

var _ managementv1connect.ProviderServiceHandler = (*ProviderServer)(nil)

// NewProviderServer wraps a providerStore + cipher in a ProviderServer. A nil
// cipher disables secret saves (CodeFailedPrecondition) while keeping reads
// available, matching the keyless-degradation posture of the web tier.
func NewProviderServer(store providerStore, cipher *crypto.Cipher, log *slog.Logger) *ProviderServer {
	if log == nil {
		log = slog.Default()
	}
	s := &ProviderServer{store: store, cipher: cipher, log: log, lookupEnv: os.Getenv, now: time.Now}
	s.resolveInvite = func(ctx context.Context, token, code string) (discordinvite.Resolved, error) {
		return discordinvite.Resolve(ctx, token, code, log)
	}
	s.checkGuildAdmin = func(ctx context.Context, token, guildID, userID string) (string, error) {
		return discordguild.CheckAdminGuild(ctx, token, guildID, userID, log)
	}
	s.voiceChannelInGuild = func(ctx context.Context, token, guildID, channelID string) (bool, error) {
		channels, err := discordshare.ListVoiceChannels(ctx, token, guildID, log)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(channels, func(c discordshare.Channel) bool { return c.ID == channelID }), nil
	}
	return s
}

// SetEnvBotToken wires the deployment's central DISCORD_BOT_TOKEN as the final
// fallback for the guild-admin proof's check token (#504) and for
// ResolveGuildInvite, so central-token tenants (no BYOK bot token saved) can
// still prove guild administration and use the invite picker. Called once at
// boot, before the server serves, so no lock is needed.
func (s *ProviderServer) SetEnvBotToken(token string) {
	s.envBotToken = token
}

// SetKeyEntitlement wires the platform-key entitlement ListProviderConfigs
// consults to report a slot as provided by the platform (W9) — the same
// instance VoiceServer.SetKeyEntitlement and every engine get, so the badge and
// the call-time resolution can never disagree. Called once at boot, before the
// server serves; nil (the default) grants everything.
func (s *ProviderServer) SetKeyEntitlement(ent llmbuild.PlatformKeyEntitlement) {
	s.keyEnt = ent
}

// SetLLMDefault wires the deployment-default LLM (#702) the Configuration read
// reports for a Tenant with no LLM Provider Config of its own. Called once at
// boot, before the server serves; unset is the code default (Groq, ADR-0036).
func (s *ProviderServer) SetLLMDefault(d llmbuild.DeploymentDefault) {
	s.llmDefault = d
}

// SetUsageSources wires the reads behind GetUsageSummary: the active plan
// (the store) and the monthly allowance (the same spend.PlanAllowance the
// session and off-session gates hold, nil where no allowance gates anything).
// Called once at boot, before the server serves; either may be nil.
func (s *ProviderServer) SetUsageSources(plans activePlanReader, allowance allowanceStateReader) {
	s.plans = plans
	s.allowance = allowance
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

// tenant is [requireTenant] (kept as a method so the handlers read as before).
func (s *ProviderServer) tenant(ctx context.Context) (uuid.UUID, error) { return requireTenant(ctx) }

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
		return nil, internalErr(s.log, "ListProviderConfigs: list provider_config failed", err)
	}
	// Index by Component: each slot is represented by its FIRST component's row
	// (ElevenLabs' stt/tts rows share one key, so either represents the slot).
	byComponent := latestByComponent(configs)
	// The LLM in effect (#702): the Tenant's most recently saved LLM row — the
	// same row a Voice Session start reads — else the deployment default.
	effective := effectiveLLM(s.llmDefault, byComponent)

	dep, err := s.store.GetDeploymentConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, internalErr(s.log, "ListProviderConfigs: get deployment_config failed", err)
	}
	// ErrNotFound → zero DeploymentConfig (all empty): the unsaved, key-needed state.

	discord := discordCredential(dep)
	// The central Bot token (ADR-0057) serves a Tenant that saved none; it is
	// deployment infrastructure, outside the platform-key entitlement.
	discord.ProvidedByPlatform = !discord.GetEverSaved() && s.envBotToken != ""
	creds := []*managementv1.ProviderCredential{discord}
	platform := s.platformKeyGate(ctx, tenantID)
	for _, slot := range byokSlots {
		cred := providerCredential(string(slot.components[0]), slot.provider, slotRow(slot, configs, byComponent))
		cred.ProvidedByPlatform = !cred.GetEverSaved() && s.lookupEnv(slot.envKey) != "" && platform(slot.components[0])
		cred.InEffect = slot.isLLM() && slot.provider == effective.GetProvider()
		creds = append(creds, cred)
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
		GuildName:            dep.GuildName,
		VoiceChannelId:       dep.VoiceChannelID,
		DiscordApplicationId: s.discordAppID,
		IntegrationState:     integrationState,
		IntegrationDetail:    integrationDetail,
		EffectiveLlm:         effective,
		EffectiveChatLlm:     s.effectiveChatLLM(configs, platform),
	}), nil
}

// isLLM reports whether the slot is a voice-LLM slot (groq, anthropic): the
// slots a Tenant chooses BETWEEN, each with its own (llm, provider) row.
func (s byokSlot) isLLM() bool {
	return len(s.components) == 1 && s.components[0] == storage.ComponentLLM
}

// latestByComponent indexes configs by Component, keeping the most recently
// updated row of each — the row storage.GetProviderConfigByComponent returns.
func latestByComponent(configs []storage.ProviderConfig) map[storage.Component]storage.ProviderConfig {
	out := make(map[storage.Component]storage.ProviderConfig, len(configs))
	for _, c := range configs {
		if cur, ok := out[c.Component]; !ok || c.UpdatedAt.After(cur.UpdatedAt) {
			out[c.Component] = c
		}
	}
	return out
}

// slotRow picks the stored row that represents slot. An LLM slot is its OWN
// (llm, provider) row — two LLM slots share the Component, so the newest row
// of the Component would show one slot's key under the other. Every other slot
// is represented by its first Component's row, as before.
func slotRow(slot byokSlot, configs []storage.ProviderConfig, byComponent map[storage.Component]storage.ProviderConfig) storage.ProviderConfig {
	if !slot.isLLM() {
		return byComponent[slot.components[0]]
	}
	for _, c := range configs {
		if c.Component == storage.ComponentLLM && c.Provider == slot.provider {
			return c
		}
	}
	return storage.ProviderConfig{}
}

// effectiveLLM resolves the voice LLM in effect for a Tenant through the one
// resolution every engine uses ([llmbuild.DeploymentDefault.Select]): its
// newest LLM row, else the deployment default. The model is the concrete one a
// voice Agent would run, never empty.
func effectiveLLM(d llmbuild.DeploymentDefault, byComponent map[storage.Component]storage.ProviderConfig) *managementv1.EffectiveLlm {
	var cfg *storage.ProviderConfig
	if row, ok := byComponent[storage.ComponentLLM]; ok {
		cfg = &row
	}
	provider, model := d.Select(cfg)
	return &managementv1.EffectiveLlm{
		Provider:        provider,
		Model:           d.VoiceModel(provider, model),
		PlatformDefault: cfg == nil,
	}
}

// platformKeyGate returns a per-call memo of "would this Tenant's call-time key
// resolution be allowed onto the env fallback" (W9). It asks
// llmbuild.ResolveKeyGated with no saved config — exactly the question every
// engine's resolution asks for an unsaved slot — so the badge derives from the
// one entitlement rule instead of re-implementing it. The entitlement is
// per-Tenant, not per-Component, so the gate is consulted at most once. A read
// error fails closed (logged, slot not reported as provided): the screen then
// shows "Key needed", the conservative answer.
func (s *ProviderServer) platformKeyGate(ctx context.Context, tenantID uuid.UUID) func(storage.Component) bool {
	var (
		asked, allowed bool
	)
	return func(component storage.Component) bool {
		if !asked {
			asked = true
			_, err := llmbuild.ResolveKeyGated(ctx, s.keyEnt, tenantID, nil, nil, component)
			switch {
			case err == nil:
				allowed = true
			case !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
				// Shared by ListProviderConfigs, GetUsageSummary and the chat
				// save path, so the log names the gate, not one caller.
				s.log.Warn("platformKeyGate: platform-key entitlement read failed", "tenant_id", tenantID, "err", err)
			}
		}
		return allowed
	}
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

	// The Planning chat is a choice, not a key slot: its own handler, its own
	// single row, and it never touches the voice LLM (ADR-0062 amendment).
	switch req.Msg.GetSlot() {
	case "":
	case chatSlot:
		return s.saveChatModel(ctx, tenantID, req.Msg.GetProvider(), req.Msg.GetModel(), req.Msg.GetSecret())
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown slot %q", req.Msg.GetSlot()))
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
		return s.saveModelOnly(ctx, tenantID, slot, req.Msg.GetModel(), req.Msg.GetActivate() && slot.isLLM())
	}
	if !isSaved(crypto.Last4(secret)) {
		// Its last four ARE the keyless placeholder: the row would be read as
		// "ride the platform key" (llmbuild.ResolveKey) while this path takes
		// any model, bypassing [ProviderServer.platformModelPriced]. No real
		// key is that short.
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is not a valid key"))
	}
	if s.cipher == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
	}

	sealed, last4, err := s.seal(secret)
	if err != nil {
		return nil, internalErr(s.log, "SaveProviderConfig: seal failed", err)
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
		return nil, internalErr(s.log, "SaveProviderConfig: upsert failed", err)
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
	activate bool,
) (*connect.Response[managementv1.SaveProviderConfigResponse], error) {
	configs, err := s.store.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		return nil, internalErr(s.log, "SaveProviderConfig: read provider_config for model-only save failed", err)
	}
	// The slot's OWN rows: (component, slot.provider). Reading "the Component's
	// newest row" instead would hand an LLM slot the other LLM slot's row.
	existing := make([]storage.ProviderConfig, 0, len(slot.components))
	for _, comp := range slot.components {
		for _, cfg := range configs {
			if cfg.Component == comp && cfg.Provider == slot.provider {
				existing = append(existing, cfg)
				break
			}
		}
	}
	if len(existing) != len(slot.components) {
		if activate {
			return s.activatePlatformSlot(ctx, tenantID, slot, model)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is required"))
	}

	if model == "" && !activate {
		return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
			Credential: providerCredential(string(slot.components[0]), slot.provider, existing[0]),
		}), nil
	}
	if slot.isLLM() && !isSaved(existing[0].CredentialsLast4) {
		// A keyless row rides the platform key: priced models only.
		if err := s.platformModelPriced(slot, model); err != nil {
			return nil, err
		}
	}
	if model == "" {
		// Activating (#702) keeps the stored model: the re-upsert below then
		// only advances updated_at, which is what makes this row the Tenant's
		// newest LLM row and so its LLM in effect.
		model = existing[0].Model
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
		return nil, internalErr(s.log, "SaveProviderConfig: model-only upsert failed", err)
	}
	// A write to an LLM slot that was NOT the Tenant's newest LLM row makes it
	// so, and with it changes which provider the LLM health entry is about: the
	// cached verdict — the other provider's — must not outlive that (#702). A
	// model save on the slot already in effect changes neither key nor provider
	// and keeps the verdict.
	if slot.isLLM() && latestByComponent(configs)[storage.ComponentLLM].Provider != slot.provider && s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}

	return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
		Credential: providerCredential(string(slot.components[0]), slot.provider, saved[0]),
	}), nil
}

// SaveDiscordSettings stores the Discord bot token (when present) and the
// non-secret Guild / Voice channel IDs (when present). Every field has wire
// presence: an omitted bot_token leaves the stored token untouched, and an
// omitted ID leaves ITS stored value untouched (#142) — so the token Save and
// the ID saves never clobber each other. The IDs no longer travel as a pair:
// guild_id alone links the guild (admin proof, clearing the Default Voice
// Channel when the guild changes), voice_channel_id alone sets the Default
// Voice Channel for an already-linked guild (the Session screen's "save as
// default" affordance) — no linked guild is CodeFailedPrecondition.
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
	// saving before the config load resolves — and clearing is unsupported (#142;
	// the guild's release path is ReleaseDiscordGuild).
	hasGuild := req.Msg.GuildId != nil
	hasChannel := req.Msg.VoiceChannelId != nil
	if hasGuild && req.Msg.GetGuildId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("guild_id must not be empty when provided"))
	}
	if hasChannel && req.Msg.GetVoiceChannelId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("voice_channel_id must not be empty when provided"))
	}
	// A present channel must be snowflake-shaped (the StartSession posture): this
	// save makes the value DURABLE, so a channel name or pasted URL persisting
	// here would break every later default-channel consumer (session starts, the
	// Players-panel member read).
	if hasChannel && !isSnowflake(req.Msg.GetVoiceChannelId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("voice_channel_id must be a Discord channel id"))
	}
	// The guild id likewise: SaveDiscordGuild clears the stored Default Voice
	// Channel whenever the incoming guild differs BYTE-wise from the stored one,
	// so a non-canonical variant of the same guild (stray whitespace, a pasted
	// decoration) must never reach that comparison — and a non-canonical stored
	// guild_id would also break the interaction router's string-equality lookup
	// (GetTenantIDByGuildID).
	if hasGuild && !isSnowflake(req.Msg.GetGuildId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("guild_id must be a Discord server id"))
	}
	if req.Msg.BotToken != nil {
		if s.cipher == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
		}
		if req.Msg.GetBotToken() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("bot_token must not be empty when provided"))
		}
	}

	// Guild-ownership proof (#504, ADR-0058): binding a guild_id requires the
	// saver to prove they administer that guild, enforced in BOTH admission
	// modes with no self-host bypass. The proof runs BEFORE any write (the token
	// save included), so a rejected request mutates nothing — and the
	// ErrGuildTaken message below is only ever reachable by proven guild admins,
	// closing the cross-tenant existence oracle. A channel-only save needs no
	// proof: the guild it defaults within was already proven at link time.
	var guildName string
	if hasGuild {
		guildName, err = s.proveGuildAdmin(ctx, tenantID, req.Msg.GetBotToken(), req.Msg.GetGuildId())
		if err != nil {
			return nil, err
		}
	}

	// A channel-only save needs an already-linked guild. Check BEFORE any write
	// (a riding bot_token included) so a rejected request mutates nothing — the
	// same pre-write posture as the proof above. SaveDefaultVoiceChannel's own
	// linked-guild fence below stays as the racing-release backstop.
	channelGuild := req.Msg.GetGuildId()
	if hasChannel && !hasGuild {
		dep, derr := s.store.GetDeploymentConfig(ctx, tenantID)
		if derr != nil && !errors.Is(derr, storage.ErrNotFound) {
			return nil, internalErr(s.log, "SaveDiscordSettings: load deployment config failed", derr)
		}
		if dep.GuildID == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("link a Discord server first"))
		}
		channelGuild = dep.GuildID
	}
	// The Default Voice Channel must be one of that guild's voice channels
	// (R9): the value is DURABLE and every later session start joins it, so a
	// foreign snowflake must never persist here. Pre-write, like the proof.
	if hasChannel {
		if err := s.checkVoiceChannelInGuild(ctx, tenantID, req.Msg.GetBotToken(), channelGuild, req.Msg.GetVoiceChannelId()); err != nil {
			return nil, err
		}
	}

	var sealed []byte
	var last4 string
	if req.Msg.BotToken != nil {
		// Seal before any write so a seal failure mutates nothing.
		sealed, last4, err = s.seal(req.Msg.GetBotToken())
		if err != nil {
			return nil, internalErr(s.log, "SaveDiscordSettings: seal failed", err)
		}
	}

	var dep storage.DeploymentConfig

	// IDs FIRST, token second: the guild write is the one that can be refused
	// as a business rule ("already linked by another tenant", ErrGuildTaken),
	// and a refused guild must leave nothing changed — the riding token
	// included. The token save then returns the full row, IDs and all. (A
	// token write failing AFTER a successful guild write is a store fault, not
	// a refusal; the guild link stands, proven with this very token.)
	//
	// IDs only when present on the wire (mirrors the token's presence handling):
	// a token-only save must never touch the stored IDs (#142).
	switch {
	case hasGuild && hasChannel:
		dep, err = s.store.SaveDiscordChannels(ctx, tenantID, req.Msg.GetGuildId(), req.Msg.GetVoiceChannelId())
	case hasGuild:
		// Guild-only link: the stored Default Voice Channel survives a re-save of
		// the same guild and is cleared on a change (it belongs to the old guild).
		dep, err = s.store.SaveDiscordGuild(ctx, tenantID, req.Msg.GetGuildId())
	case hasChannel:
		// Default-Voice-Channel-only save (the Session screen's "save as
		// default"): needs an already-linked guild.
		dep, err = s.store.SaveDefaultVoiceChannel(ctx, tenantID, req.Msg.GetVoiceChannelId())
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("link a Discord server first"))
		}
	}
	if hasGuild || hasChannel {
		if errors.Is(err, storage.ErrGuildTaken) {
			// First-registrar-wins guild binding (#483) + the #504 proof above:
			// only a PROVEN admin of the guild can even reach this message, so it
			// leaks no cross-tenant existence to strangers. A deliberate
			// precondition refusal, never a silent rebind; the release path for a
			// legitimate transfer is ReleaseDiscordGuild.
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("this Discord server is already linked by another tenant; it must unlink there first"))
		}
		if err != nil {
			return nil, internalErr(s.log, "SaveDiscordSettings: save channels failed", err)
		}
	}
	// The server's name, read by the proof above, is stored with the link so
	// Setup names the server instead of showing its id. Display data only: a
	// failed write is logged and the link stands (the screen falls back to the
	// id).
	if hasGuild && guildName != "" {
		if _, nerr := s.store.SaveDiscordGuildName(ctx, tenantID, req.Msg.GetGuildId(), guildName); nerr != nil {
			s.log.Warn("SaveDiscordSettings: store guild name failed", "tenant_id", tenantID, "err", nerr)
		} else {
			dep.GuildName = guildName
		}
	}

	if req.Msg.BotToken != nil {
		dep, err = s.store.SaveDiscordBotToken(ctx, tenantID, sealed, last4)
		if err != nil {
			return nil, internalErr(s.log, "SaveDiscordSettings: save token failed", err)
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

// proveGuildAdmin runs the #504 guild-ownership proof for a SaveDiscordSettings
// that binds guildID: the authenticated saver (auth.CurrentUser — the session
// principal, ADR-0055, never an env allowlist) must administer the guild per
// the checkGuildAdmin seam, and returns the guild's display name the check
// read. reqToken is the request's plaintext Bot token when
// one rode along (pre-seal), else the check-token ladder falls back to the
// stored BYOK token and finally the central env token — the same token that
// will serve the guild (BYOK + central-token tenants both covered). Error
// granularity (no membership oracle): Bot-cannot-see-guild is FailedPrecondition
// "not a member"; user-not-in-guild and member-without-perms collapse to ONE
// PermissionDenied message.
func (s *ProviderServer) proveGuildAdmin(ctx context.Context, tenantID uuid.UUID, reqToken, guildID string) (string, error) {
	user, ok := auth.CurrentUser(ctx)
	if !ok || user.DiscordUserID == "" {
		return "", connect.NewError(connect.CodeUnauthenticated,
			errors.New("no authenticated Discord user to verify guild permissions for"))
	}

	token, err := s.checkToken(ctx, tenantID, reqToken)
	if err != nil {
		return "", err
	}

	name, err := s.checkGuildAdmin(ctx, token, guildID, user.DiscordUserID)
	switch {
	case err == nil:
		return name, nil
	case errors.Is(err, discordguild.ErrBotNotInGuild):
		return "", connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the Bot is not a member of that server"))
	case errors.Is(err, discordguild.ErrUserNotInGuild), errors.Is(err, discordguild.ErrNoPermission):
		return "", connect.NewError(connect.CodePermissionDenied,
			errors.New("you need the Manage Server permission in that Discord server to link it"))
	default:
		return "", internalErr(s.log, "SaveDiscordSettings: guild-admin proof failed", err)
	}
}

// checkToken walks the ADR-0058 check-token ladder for a read-only Discord
// REST call made on a save's behalf: the request's plaintext Bot token when one
// rode along (pre-seal), else the stored BYOK token, else the central env
// token. No token on any rung is FailedPrecondition ("save the token first").
func (s *ProviderServer) checkToken(ctx context.Context, tenantID uuid.UUID, reqToken string) (string, error) {
	token := reqToken
	if token == "" {
		stored, err := s.resolveBotToken(ctx, tenantID)
		if err != nil {
			return "", err
		}
		token = stored
	}
	if token == "" {
		token = s.envBotToken
	}
	if token == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			errors.New("save the Discord bot token first"))
	}
	return token, nil
}

// checkVoiceChannelInGuild verifies a Default Voice Channel save names one of
// guildID's voice channels (R9) — the guild being linked in the same request,
// or the already-linked one for a channel-only save — listed with the
// check-token ladder's token. Not in the guild is the GM-readable
// InvalidArgument StartSession also uses.
func (s *ProviderServer) checkVoiceChannelInGuild(ctx context.Context, tenantID uuid.UUID, reqToken, guildID, channelID string) error {
	token, err := s.checkToken(ctx, tenantID, reqToken)
	if err != nil {
		return err
	}
	linked, err := s.voiceChannelInGuild(ctx, token, guildID, channelID)
	switch {
	case errors.Is(err, discordshare.ErrNoAccess):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the Bot is not a member of that server"))
	case err != nil:
		return internalErr(s.log, "SaveDiscordSettings: list guild voice channels failed", err)
	}
	if !linked {
		return connect.NewError(connect.CodeInvalidArgument, errVoiceChannelNotLinked)
	}
	return nil
}

// ReleaseDiscordGuild frees this Tenant's bound Guild (#504, ADR-0058) — the
// supported path for a legitimate guild transfer (A releases, B saves with
// proof). The request must echo the currently-bound guild_id; storage does an
// atomic compare-and-clear, so a stale echo mutates nothing. NO Discord proof is
// required here: the operator may have lost guild access, and release is
// tenant-local cleanup of the caller's OWN binding. On success the health cache
// is busted and the standing presence reconciled (tears down the freed guild's
// presence — required before the next tenant claims it).
func (s *ProviderServer) ReleaseDiscordGuild(
	ctx context.Context,
	req *connect.Request[managementv1.ReleaseDiscordGuildRequest],
) (*connect.Response[managementv1.ReleaseDiscordGuildResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	guildID := req.Msg.GetGuildId()
	if guildID == "" {
		// Present-but-empty is an accident (mirrors #142's posture), never a
		// wildcard release.
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("guild_id must be the currently linked Discord server"))
	}

	dep, err := s.store.ReleaseDiscordGuild(ctx, tenantID, guildID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("that Discord server is not the one currently linked; reload and try again"))
	}
	if err != nil {
		return nil, internalErr(s.log, "ReleaseDiscordGuild: store release failed", err)
	}

	// The binding changed: bust the cached health verdict and reconcile the
	// standing presence so the freed guild's client tears down (#489 semantics —
	// critical for a transfer, or the old presence squats the guild).
	if s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}
	if s.refreshPresence != nil {
		go s.refreshPresence(tenantID)
	}

	return connect.NewResponse(&managementv1.ReleaseDiscordGuildResponse{
		GuildId:        dep.GuildID,
		VoiceChannelId: dep.VoiceChannelID,
	}), nil
}

// ResolveGuildInvite resolves a pasted Discord invite code to its Guild and that
// Guild's voice channels server-side (#105, ADR-0047). It is a no-side-effects
// read: the resolver makes only Discord REST GETs, and no state is written. The
// Bot token follows the ADR-0058 ladder minus the request rung (the request
// carries only the code): decrypted stored BYOK token > central env token — so
// central-token tenants can use the picker. The code is validated + path-escaped
// before the call; ErrNotFound → NotFound, ErrNoAccess → FailedPrecondition
// ("not a member"), and no token on any rung → FailedPrecondition ("save the
// token first").
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
		token = s.envBotToken
	}
	if token == "" {
		// No token on any rung (stored BYOK or central env): the resolver cannot
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
		return nil, internalErr(s.log, "GetSpendCaps: store read failed", err)
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
		if err := validateSpendCap(*v, "soft"); err != nil {
			return nil, err
		}
		soft := *v
		caps.SoftUSD = &soft
	}
	if v := req.Msg.HardUsd; v != nil {
		if err := validateSpendCap(*v, "hard"); err != nil {
			return nil, err
		}
		hard := *v
		caps.HardUSD = &hard
	}
	if caps.SoftUSD != nil && caps.HardUSD != nil && *caps.HardUSD < *caps.SoftUSD {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hard cap must be greater than or equal to the soft cap"))
	}

	if err := s.store.SetTenantSpendCaps(ctx, tenantID, caps); err != nil {
		return nil, internalErr(s.log, "SetSpendCaps: store write failed", err)
	}
	return connect.NewResponse(&managementv1.SetSpendCapsResponse{Caps: toProtoSpendCaps(caps)}), nil
}

// validateSpendCap rejects a negative or non-finite cap. NaN and ±Inf are legal
// protobuf doubles ("NaN" / "Infinity" on the JSON wire) but they poison every
// comparison downstream: NaN compares false against everything, so a NaN hard cap
// would never trip the meter, and the ADR-0055 plan-allowance rung — which only
// tightens the cap when the remaining allowance is BELOW it — would never engage
// either. Refuse them at the edge, before the value reaches the row.
func validateSpendCap(v float64, which string) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s cap must be a finite number", which))
	}
	if v < 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s cap must not be negative", which))
	}
	return nil
}

// toProtoSpendCaps maps storage caps onto the wire view: a nil pointer stays absent
// (no presence), so the screen distinguishes "unset" from "0".
func toProtoSpendCaps(c storage.SpendCaps) *managementv1.SpendCaps {
	return &managementv1.SpendCaps{SoftUsd: c.SoftUSD, HardUsd: c.HardUSD}
}

// resolveBotToken resolves the stored BYOK Bot token — the stored rung of the
// ADR-0058 ladder (proveGuildAdmin, ResolveGuildInvite) — mirroring
// VoiceServer.resolveDiscordToken: no row / ENV placeholder -> "" (callers fall
// back to the central env token, then FailedPrecondition), a real saved token ->
// decrypted plaintext, a saved token with no cipher -> FailedPrecondition.
func (s *ProviderServer) resolveBotToken(ctx context.Context, tenantID uuid.UUID) (string, error) {
	dep, err := s.store.GetDeploymentConfig(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", internalErr(s.log, "resolveBotToken: store read failed", err)
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
		return "", internalErr(s.log, "openKey: decrypt failed", err)
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

// activatePlatformSlot makes an LLM slot the Tenant never saved a key for its
// LLM in effect (#702). That is only possible when the platform provides the
// slot — the deployment has the provider's env key AND the Tenant is entitled
// to the Platform Keys, the exact condition ListProviderConfigs reports as
// provided_by_platform. The choice is stored as a keyless row carrying the
// "env" placeholder, inserted only when the slot has no row at all (never an
// overwrite, #712 item 3): every key resolution reads it as "ride the env key" and
// still consults the entitlement at call time, so it grants nothing the Tenant
// did not already have. Otherwise there is no key to run on: InvalidArgument,
// like any model-only save without a stored key, and nothing is written.
func (s *ProviderServer) activatePlatformSlot(ctx context.Context, tenantID uuid.UUID, slot byokSlot, model string) (*connect.Response[managementv1.SaveProviderConfigResponse], error) {
	if s.lookupEnv(slot.envKey) == "" || !s.platformKeyGate(ctx, tenantID)(slot.components[0]) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is required"))
	}
	if err := s.platformModelPriced(slot, model); err != nil {
		return nil, err
	}
	// Insert-only (#712 item 3): the "no row" this path was chosen on was read
	// a moment ago, and a key saved since must never be overwritten by an empty
	// credential. A row that appeared in between is returned as it is: a key
	// save always activates its slot, and a concurrent activation wrote what
	// this one would have.
	saved, _, err := s.store.InsertKeylessProviderConfig(ctx, storage.NewProviderConfig{
		TenantID:  tenantID,
		Component: slot.components[0],
		Provider:  slot.provider,
		Model:     model,
	})
	if err != nil {
		return nil, internalErr(s.log, "SaveProviderConfig: activate platform slot failed", err)
	}
	if s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}
	return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
		Credential: providerCredential(string(slot.components[0]), slot.provider, saved),
	}), nil
}

// platformModelPriced refuses a model the price table has no row for on a slot
// that rides the PLATFORM key (#702 review). The spend meter prices an unknown
// (provider, model) at one flat fallback, and the plan allowance is gated on
// that estimate: for an Opus-class id the fallback is BELOW the real rate (the
// platform pays the difference, past the allowance), for a cheap snapshot id it
// is dozens of times ABOVE it (the Tenant's allowance is gone in a few turns).
// Only an exactly priced model is metered at what it costs, so only those run
// on a key the platform pays for. The empty model is the default
// ([llmbuild.DeploymentDefault.Select]) and always allowed, and so is the model
// that default resolves to: it is the operator's explicit choice, what a
// row-less Tenant already runs and what the catalog lists first (an unpriced
// one is warned about at boot). A Tenant's own key keeps free-text entry — its
// spend is its own bill.
func (s *ProviderServer) platformModelPriced(slot byokSlot, model string) error {
	if model == "" || model == s.llmDefault.VoiceModel(slot.provider, "") ||
		spend.LLMPriced(observe.Provider(slot.provider), model) {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"model %q has no price on record and cannot run on the platform key: choose a listed model, or save your own %s key to use it",
		model, slot.provider))
}

// voiceLLMSlot is the DeleteProviderConfig slot that removes every voice LLM
// row: the way back to the platform default (ADR-0004 amendment 2026-11-26).
const voiceLLMSlot = "voice_llm"

// voiceLLMDeleteSlot maps a DeleteProviderConfig slot onto the voice LLM
// providers it removes: one voice LLM slot by its provider id, or all of them
// for voiceLLMSlot. ok is false for any other slot.
func voiceLLMDeleteSlot(slot string) (providers []string, ok bool) {
	if slot == voiceLLMSlot {
		for _, s := range byokSlots {
			if s.isLLM() {
				providers = append(providers, s.provider)
			}
		}
		return providers, true
	}
	if s, found := slotFor(slot); found && s.isLLM() {
		return []string{s.provider}, true
	}
	return nil, false
}

// deleteVoiceLLM removes the Tenant's voice LLM rows of providers — a saved
// key or a keyless platform choice — so its sessions run on the newest LLM row
// left, else the platform default (ADR-0004 amendment 2026-11-26). Nothing is
// re-sealed and no secret is read.
//
// Two consequences are handled here, because the screen cannot:
//   - the provider-health cache is busted: its LLM entry was about the LLM in
//     effect, which may have just changed;
//   - the Planning chat is re-validated. A chat choice may name a model with
//     no price on record only because it ran on the Tenant's own key for that
//     provider (provider_chat.go); with that key gone it would fall to the
//     Platform Key, which only runs priced models. Its model is then reset to
//     the provider's chat default (priced by test) and the response says so.
func (s *ProviderServer) deleteVoiceLLM(ctx context.Context, tenantID uuid.UUID, providers []string) (*connect.Response[managementv1.DeleteProviderConfigResponse], error) {
	removed, err := s.store.DeleteTenantLLMConfigs(ctx, tenantID, providers)
	if err != nil {
		return nil, internalErr(s.log, "DeleteProviderConfig: delete voice llm rows failed", err)
	}
	if removed == 0 {
		// Idempotent: nothing was stored, nothing changed.
		return connect.NewResponse(&managementv1.DeleteProviderConfigResponse{}), nil
	}
	if s.invalidateHealth != nil {
		s.invalidateHealth(tenantID)
	}

	configs, err := s.store.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		return nil, internalErr(s.log, "DeleteProviderConfig: re-read provider_config failed", err)
	}
	// The deployment's own default, the same resolution the chat engine runs
	// (provider_chat.go effectiveChatLLM), so this check and the chat agree.
	sel := s.llmDefault.SelectChat(nil, configs)
	if sel.Source != llmbuild.ChatSourceChoice || sel.KeySource != llmbuild.ChatKeyPlatform ||
		chatModelPriced(sel.Provider, sel.StoredModel) == nil {
		return connect.NewResponse(&managementv1.DeleteProviderConfigResponse{}), nil
	}
	// A keyless model-only chat save: the credential columns are not written,
	// so this can never touch a chat-only key (there is none on this rung).
	if _, err := s.store.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{
		TenantID: tenantID, Provider: sel.Provider, Model: "",
	}); err != nil {
		return nil, internalErr(s.log, "DeleteProviderConfig: reset unpriced chat model failed", err)
	}
	return connect.NewResponse(&managementv1.DeleteProviderConfigResponse{ChatModelReset: true}), nil
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
