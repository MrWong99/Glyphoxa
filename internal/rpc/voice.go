package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/discordtag"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// VoiceService (#70) serves the LIVE provider data the Configuration + Campaign
// screens render: the ElevenLabs voice catalog, a short voice preview, the Groq
// model allowlist, and the async provider-health signal. Every live call uses
// the operator's decrypted BYOK key (ADR-0004 credential bridge, hybrid policy
// ADR-0039 — a real saved key overrides ENV, the "env" placeholder falls back to
// the adapter's own *_API_KEY). The live ElevenLabs / Groq / Discord seams are
// function fields so unit tests fake them and the default `go test` makes no
// network call (ADR-0021).

// defaultPreviewText is spoken when PreviewVoice is called with empty text.
const defaultPreviewText = "Hello! I'm your voice for this campaign. Roll for initiative."

// previewTimeout bounds a single preview synthesis so a black-holed TTS endpoint
// cannot hold the handler open; the request context's own deadline still wins
// when shorter.
const previewTimeout = 15 * time.Second

// healthCheckTimeout bounds each provider's health test-call, so a hung
// provider degrades that one badge instead of stalling the whole health probe.
// The checks run concurrently (#150), so this also bounds the whole RPC.
const healthCheckTimeout = 12 * time.Second

// healthCacheTTL is how long a GetProviderHealth result is served from the
// server-side cache (#150). The SPA refires the RPC on every window focus;
// within the TTL those refetches cost zero vendor calls.
const healthCacheTTL = 60 * time.Second

// healthProbeTimeout is the hard deadline on the WHOLE health probe — store
// reads included, which healthCheckTimeout does not cover. The probe runs
// while the tenant's cache entry lock is held, so without this bound one hung
// store read would wedge every later health call for the tenant.
const healthProbeTimeout = healthCheckTimeout + 3*time.Second

// voiceStore is the narrow read surface VoiceServer needs to resolve the tenant
// BYOK keys. *storage.Store satisfies it; tests drive a fake.
type voiceStore interface {
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
}

// VoiceServer implements managementv1connect.VoiceServiceHandler. The live
// adapter constructors and the Groq/Discord pings are seams (function fields)
// defaulted by NewVoiceServer and overridden by unit tests.
type VoiceServer struct {
	store  voiceStore
	cipher *crypto.Cipher
	log    *slog.Logger

	// keyEnt gates the env fallback on every provider-key resolution (ADR-0054
	// seam (a)); nil grants everything. Wired via SetKeyEntitlement.
	keyEnt llmbuild.PlatformKeyEntitlement

	// newLister builds a TTS voice catalog client for an API key ("" -> the
	// adapter's env fallback). Defaults to ElevenLabs.
	newLister func(apiKey string) tts.VoiceLister
	// newSynth builds a TTS synthesizer for an API key. Defaults to ElevenLabs.
	newSynth func(apiKey string) tts.Synthesizer
	// pingLLM is the Groq liveness test-call (a real key -> nil). Defaults to a
	// GET against the Groq models endpoint.
	pingLLM func(ctx context.Context, apiKey string) error
	// pingImage is the Gemini image-provider liveness test-call (#311). Defaults
	// to a GET against Gemini's OpenAI-compat models endpoint; unit tests fake it.
	pingImage func(ctx context.Context, apiKey string) error
	// listModels fetches a provider's live model catalog for the model select
	// (#227). Defaults to the Groq OpenAI-compatible GET /models via the shared
	// adapter; unit tests fake it so the default `go test` makes no vendor call.
	listModels func(ctx context.Context, apiKey string) ([]string, error)
	// botTag proves the Discord token via REST (GET /users/@me — no gateway
	// IDENTIFY, #150) and returns the bot tag. Defaults to discordtag.Resolve.
	botTag func(ctx context.Context, token string) (string, error)

	// now is the health cache's clock; tests advance it past the TTL.
	now func() time.Time
	// probeTimeout is the whole-probe hard deadline (healthProbeTimeout in
	// prod); tests shrink it to pin the hung-dependency path.
	probeTimeout time.Duration

	// sessionActive reports whether a live voice session is running (#150):
	// the Discord health check then short-circuits to healthy without touching
	// Discord — the session on the same token IS the health signal, and a probe
	// would race its reconnects for the per-token IDENTIFY budget. nil (not
	// wired, e.g. web-only mode has no in-process loop to consult) means the
	// probe always runs.
	sessionActive func() bool

	// healthMu guards healthCache: one TTL-cached GetProviderHealth result per
	// tenant (#150). Probes are singleflighted per entry: one leader probes,
	// concurrent callers wait on that SAME probe (each with its own ctx
	// bail-out) and are handed its result — never a probe of their own.
	healthMu    sync.Mutex
	healthCache map[uuid.UUID]*healthEntry
}

// healthEntry is one tenant's cached provider-health result. mu guards the
// fields and is only held for state flips — never across a probe.
type healthEntry struct {
	mu        sync.Mutex
	at        time.Time // zero until the first probe lands
	providers []*managementv1.ProviderHealth
	// botTag is the last tag a successful Discord probe resolved. It outlives
	// the TTL so the active-session short-circuit (which does not touch
	// Discord) can keep reporting "Connected as X#NNNN" instead of blanking
	// the row for the whole session.
	botTag string
	// inflight is non-nil while a leader's probe runs and is closed when it
	// finishes; callers arriving meanwhile wait on it instead of probing.
	inflight chan struct{}
	// lastProbe is the most recent probe's result — set even when a timed-out
	// probe is not cached, so waiters on that probe still get its answer.
	lastProbe []*managementv1.ProviderHealth
}

var _ managementv1connect.VoiceServiceHandler = (*VoiceServer)(nil)

// NewVoiceServer wires a VoiceServer with the live ElevenLabs / Groq / Discord
// seams. A nil cipher disables decrypting saved keys (the env-fallback path still
// works); the live calls then surface the adapters' missing-key errors.
func NewVoiceServer(store voiceStore, cipher *crypto.Cipher, log *slog.Logger) *VoiceServer {
	if log == nil {
		log = slog.Default()
	}
	return &VoiceServer{
		store:        store,
		cipher:       cipher,
		log:          log,
		newLister:    func(apiKey string) tts.VoiceLister { return elevenlabs.New(apiKey) },
		newSynth:     func(apiKey string) tts.Synthesizer { return elevenlabs.New(apiKey) },
		pingLLM:      livePingGroq,
		pingImage:    livePingGemini,
		listModels:   func(ctx context.Context, apiKey string) ([]string, error) { return groq.New(apiKey).ListModels(ctx) },
		botTag:       func(ctx context.Context, token string) (string, error) { return discordtag.Resolve(ctx, token, log) },
		now:          time.Now,
		probeTimeout: healthProbeTimeout,
		healthCache:  map[uuid.UUID]*healthEntry{},
	}
}

// activeSessionSource reports the live voice session, if any.
// *session.Manager satisfies it; tests drive a fake.
type activeSessionSource interface {
	Snapshot() (storage.VoiceSession, bool)
}

// SetSessions wires the live session source the Discord health check consults
// (#150). Called once at boot, after the session manager exists and before the
// server serves, so no lock is needed.
func (s *VoiceServer) SetSessions(src activeSessionSource) {
	s.sessionActive = func() bool {
		_, active := src.Snapshot()
		return active
	}
}

// SetKeyEntitlement wires the platform-key entitlement every provider-key
// resolution consults before riding the env fallback (ADR-0054 seam (a),
// ADR-0055). Called once at boot before the server serves, so no lock is
// needed; nil (the default) grants everything — the allowlist/self-host
// posture. The Discord Bot token paths are deployment infrastructure and stay
// outside the entitlement.
func (s *VoiceServer) SetKeyEntitlement(ent llmbuild.PlatformKeyEntitlement) {
	s.keyEnt = ent
}

// Handler builds the Connect HTTP handler for VoiceService and returns its mount
// path + handler, mirroring the other rpc servers.
func (s *VoiceServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewVoiceServiceHandler(s, opts...)
}

func (s *VoiceServer) tenant(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.TenantID(ctx)
	if !ok {
		return uuid.Nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}
	return id, nil
}

// ListModels returns the live Groq model catalog for the model select (#227):
// the OpenAI-compatible GET /models fetched with the tenant's decrypted BYOK key
// (the ListVoices / health-check posture, ADR-0039). The default model is pinned
// first, the rest are sorted and deduped, and the previously-hardcoded allowlist
// (with its deprecated ids) is gone. A failed fetch degrades gracefully — the
// screen stays usable — by returning just the default and NO rpc error, since the
// combobox accepts free-text entry regardless. An unknown provider is a client
// error (unchanged).
func (s *VoiceServer) ListModels(
	ctx context.Context,
	req *connect.Request[managementv1.ListModelsRequest],
) (*connect.Response[managementv1.ListModelsResponse], error) {
	if req.Msg.GetProvider() != "groq" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("no model catalog for provider %q", req.Msg.GetProvider()))
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentLLM)
	if err != nil {
		return nil, err
	}

	models, err := s.listModels(ctx, key)
	if err != nil {
		// Never fail the RPC: a broken catalog must not break the Configuration
		// screen (free-text entry still saves and runs, ADR-0039). Fall back to the
		// pinned default so the select still renders.
		s.log.Warn("ListModels: live Groq catalog failed", "err", err)
		return connect.NewResponse(&managementv1.ListModelsResponse{Models: []string{groq.DefaultModel}}), nil
	}
	return connect.NewResponse(&managementv1.ListModelsResponse{Models: groqCatalog(models)}), nil
}

// groqCatalog shapes a raw Groq /models catalog for the select: the default model
// is pinned first (even if the live list omits or re-orders it), and the rest are
// deduped and sorted ascending. Curation of non-chat ids is deliberately NOT done
// — the pinned default plus free-text entry make the extra ids harmless, and the
// staleness the static allowlist caused is exactly what this removes (#227).
func groqCatalog(models []string) []string {
	out := []string{groq.DefaultModel}
	seen := map[string]bool{groq.DefaultModel: true}
	rest := make([]string, 0, len(models))
	for _, m := range models {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		rest = append(rest, m)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// ListVoices maps the ElevenLabs voice catalog onto the wire type for the voice
// select, using the decrypted tenant TTS key. A live failure (bad/missing key)
// is CodeUnavailable — the screen degrades to the persisted voice id.
func (s *VoiceServer) ListVoices(
	ctx context.Context,
	_ *connect.Request[managementv1.ListVoicesRequest],
) (*connect.Response[managementv1.ListVoicesResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentTTS)
	if err != nil {
		return nil, err
	}

	voices, err := s.newLister(key).ListVoices(ctx)
	if err != nil {
		s.log.Warn("ListVoices: live catalog failed", "err", err)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("voice catalog is unavailable"))
	}

	out := make([]*managementv1.Voice, 0, len(voices))
	for _, v := range voices {
		out = append(out, toProtoVoice(v))
	}
	return connect.NewResponse(&managementv1.ListVoicesResponse{Voices: out}), nil
}

// PreviewVoice synthesizes a short sample for one voice and returns it as a WAV
// blob the browser can play directly (wraps the existing Synthesize).
func (s *VoiceServer) PreviewVoice(
	ctx context.Context,
	req *connect.Request[managementv1.PreviewVoiceRequest],
) (*connect.Response[managementv1.PreviewVoiceResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	voiceID := req.Msg.GetVoiceId()
	if voiceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("voice_id is required"))
	}
	text := req.Msg.GetText()
	if text == "" {
		text = defaultPreviewText
	}

	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentTTS)
	if err != nil {
		return nil, err
	}

	synCtx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()

	ch, err := s.newSynth(key).Synthesize(synCtx, tts.SynthesizeRequest{
		Sentence: text,
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: voiceID},
	})
	if err != nil {
		s.log.Warn("PreviewVoice: synthesis failed to start", "err", err)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("voice preview is unavailable"))
	}

	var (
		pcm        []byte
		sampleRate = 24000
		channels   = 1
	)
	for chunk := range ch {
		if len(pcm) == 0 && chunk.SampleRate > 0 {
			sampleRate, channels = chunk.SampleRate, chunk.Channels
		}
		pcm = append(pcm, chunk.PCM...)
	}
	if channels == 0 {
		channels = 1
	}

	return connect.NewResponse(&managementv1.PreviewVoiceResponse{
		Audio:      encodeWAV(pcm, sampleRate, channels),
		SampleRate: int32(sampleRate),
		Channels:   int32(channels),
		MimeType:   "audio/wav",
	}), nil
}

// GetProviderHealth runs the per-provider test-calls and returns each slot's
// tested status plus the resolved Discord bot tag. A provider with no resolvable
// key (or a failing test-call) is reported degraded; the screen still renders
// key-presence instantly and only upgrades the badge from this.
//
// The result is cached per tenant for healthCacheTTL (#150): the SPA refires
// this RPC on every window focus, and within the TTL those refetches are
// answered from cache without touching any vendor.
func (s *VoiceServer) GetProviderHealth(
	ctx context.Context,
	_ *connect.Request[managementv1.GetProviderHealthRequest],
) (*connect.Response[managementv1.GetProviderHealthResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	e := s.healthEntry(tenantID)
	e.mu.Lock()
	if !e.at.IsZero() && s.now().Sub(e.at) < healthCacheTTL {
		providers := e.providers
		e.mu.Unlock()
		return connect.NewResponse(&managementv1.GetProviderHealthResponse{Providers: providers}), nil
	}

	// Singleflight: if a probe is already in flight, wait on THAT probe (with
	// this caller's own ctx bail-out) and take its result — never run a second
	// probe for the same tenant concurrently or queue behind it for a fresh one.
	if ch := e.inflight; ch != nil {
		e.mu.Unlock()
		select {
		case <-ch:
			e.mu.Lock()
			providers := e.lastProbe
			e.mu.Unlock()
			return connect.NewResponse(&managementv1.GetProviderHealthResponse{Providers: providers}), nil
		case <-ctx.Done():
			return nil, ctxError(ctx)
		}
	}

	// A caller that is already gone must not become a probe leader and burn a
	// probeTimeout slot on vendors nobody will see.
	if ctx.Err() != nil {
		e.mu.Unlock()
		return nil, ctxError(ctx)
	}

	// Become the leader: mark the flight, release the lock, probe.
	ch := make(chan struct{})
	e.inflight = ch
	lastBotTag := e.botTag
	e.mu.Unlock()

	// Probe detached from the request's cancellation (values, e.g. the tenant,
	// survive): a focus-refetch the browser aborts mid-probe must not poison the
	// cache with cancellation errors — and waiters on this flight still need the
	// answer. probeTimeout is the hard deadline on the whole probe — store reads
	// included — so a hung dependency cannot wedge the tenant's health path.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.probeTimeout)
	defer cancel()
	providers, complete := s.probeProviders(pctx, tenantID, lastBotTag)

	e.mu.Lock()
	e.lastProbe = providers
	if complete {
		// Only a finished probe is cached: a timed-out one would pin
		// "degraded: deadline exceeded" for the whole TTL, so the next call
		// retries instead.
		e.providers, e.at = providers, s.now()
		// Remember the last successfully resolved bot tag for the
		// active-session short-circuit; a degraded probe keeps the old one.
		for _, p := range providers {
			if p.GetProvider() == "discord" && p.GetBotTag() != "" {
				e.botTag = p.GetBotTag()
			}
		}
	}
	e.inflight = nil
	e.mu.Unlock()
	close(ch) // release the waiters onto lastProbe

	return connect.NewResponse(&managementv1.GetProviderHealthResponse{Providers: providers}), nil
}

// ctxError maps a done request context onto the matching Connect error.
func ctxError(ctx context.Context) error {
	code := connect.CodeCanceled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = connect.CodeDeadlineExceeded
	}
	return connect.NewError(code, ctx.Err())
}

// InvalidateHealth drops tenantID's cached health result (#150): called after
// a credential save so a stale Degraded badge cannot outlive the fix for up to
// a TTL — the next health call probes the vendors with the new key. The
// last-known bot tag is dropped too (a new token may be a different bot). Safe
// under concurrent RPCs: an in-flight probe writes into its own (now orphaned)
// entry and the next call starts fresh.
func (s *VoiceServer) InvalidateHealth(tenantID uuid.UUID) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	delete(s.healthCache, tenantID)
}

// healthEntry returns tenantID's cache slot, creating it on first use.
func (s *VoiceServer) healthEntry(tenantID uuid.UUID) *healthEntry {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	e, ok := s.healthCache[tenantID]
	if !ok {
		e = &healthEntry{}
		s.healthCache[tenantID] = e
	}
	return e
}

// probeProviders runs the three per-provider test-calls CONCURRENTLY (#150):
// the worst case is the slowest single check, not the sum of all three. When
// ctx expires before every check reports, the still-missing slots are filled
// with a degraded timeout result, the stuck goroutines are abandoned (their
// sends land in the buffered channel and are dropped), and complete=false
// tells the caller not to cache.
func (s *VoiceServer) probeProviders(ctx context.Context, tenantID uuid.UUID, lastBotTag string) (providers []*managementv1.ProviderHealth, complete bool) {
	checks := []struct {
		name string
		run  func(context.Context, uuid.UUID) *managementv1.ProviderHealth
	}{
		{"groq", s.healthLLM},
		{"elevenlabs", s.healthTTS},
		{"gemini", s.healthImage},
		{"discord", func(ctx context.Context, tenantID uuid.UUID) *managementv1.ProviderHealth {
			return s.healthDiscord(ctx, tenantID, lastBotTag)
		}},
	}

	type slot struct {
		i int
		h *managementv1.ProviderHealth
	}
	results := make(chan slot, len(checks))
	for i, check := range checks {
		go func() {
			results <- slot{i, check.run(ctx, tenantID)}
		}()
	}

	providers = make([]*managementv1.ProviderHealth, len(checks))
	for range checks {
		select {
		case r := <-results:
			providers[r.i] = r.h
		case <-ctx.Done():
			// Drain checks that finished in the same instant, then mark the
			// rest timed out.
			for {
				select {
				case r := <-results:
					providers[r.i] = r.h
					continue
				default:
				}
				break
			}
			for i, check := range checks {
				if providers[i] == nil {
					providers[i] = degraded(check.name, fmt.Errorf("health probe timed out: %w", ctx.Err()))
				}
			}
			return providers, false
		}
	}
	return providers, true
}

// healthLLM pings Groq with the decrypted LLM key.
func (s *VoiceServer) healthLLM(ctx context.Context, tenantID uuid.UUID) *managementv1.ProviderHealth {
	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentLLM)
	if err != nil {
		return degraded("groq", err)
	}
	cctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	if err := s.pingLLM(cctx, key); err != nil {
		return degraded("groq", err)
	}
	return healthy("groq")
}

// healthImage pings the Gemini image provider with the decrypted image key (#311),
// mirroring healthLLM. No configured key (nor GEMINI_API_KEY fallback) → degraded.
func (s *VoiceServer) healthImage(ctx context.Context, tenantID uuid.UUID) *managementv1.ProviderHealth {
	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentImage)
	if err != nil {
		return degraded("gemini", err)
	}
	cctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	if err := s.pingImage(cctx, key); err != nil {
		return degraded("gemini", err)
	}
	return healthy("gemini")
}

// healthTTS reuses ListVoices as the ElevenLabs liveness probe (GET /v1/voices).
func (s *VoiceServer) healthTTS(ctx context.Context, tenantID uuid.UUID) *managementv1.ProviderHealth {
	key, err := s.resolveComponentKey(ctx, tenantID, storage.ComponentTTS)
	if err != nil {
		return degraded("elevenlabs", err)
	}
	cctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	if _, err := s.newLister(key).ListVoices(cctx); err != nil {
		return degraded("elevenlabs", err)
	}
	return healthy("elevenlabs")
}

// healthDiscord proves the Bot token via REST (GET /users/@me, no gateway
// IDENTIFY — #150) and reports the resolved bot tag. While a voice session is
// active the check short-circuits to healthy without touching Discord — the
// live session runs on the same token, so it IS the health signal — carrying
// lastBotTag (the tag of the last successful probe) so the screen's
// "Connected as X#NNNN" row survives the session.
func (s *VoiceServer) healthDiscord(ctx context.Context, tenantID uuid.UUID, lastBotTag string) *managementv1.ProviderHealth {
	if s.sessionActive != nil && s.sessionActive() {
		h := healthy("discord")
		h.BotTag = lastBotTag
		h.Detail = "live voice session active"
		return h
	}
	token, err := s.resolveDiscordToken(ctx, tenantID)
	if err != nil {
		return degraded("discord", err)
	}
	cctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	tag, err := s.botTag(cctx, token)
	if err != nil {
		return degraded("discord", err)
	}
	h := healthy("discord")
	h.BotTag = tag
	return h
}

func healthy(provider string) *managementv1.ProviderHealth {
	return &managementv1.ProviderHealth{Provider: provider, Status: managementv1.HealthStatus_HEALTH_STATUS_HEALTHY}
}

func degraded(provider string, err error) *managementv1.ProviderHealth {
	return &managementv1.ProviderHealth{
		Provider: provider,
		Status:   managementv1.HealthStatus_HEALTH_STATUS_DEGRADED,
		Detail:   err.Error(),
	}
}

// resolveComponentKey resolves a component's BYOK key under the hybrid policy:
// no row / "env" placeholder -> "" (adapter env fallback); a real saved key ->
// decrypted plaintext; a saved key with no cipher -> FailedPrecondition.
//
// The env fallback spends the deployment's Platform Keys (health pings,
// catalogs, TTS preview), so it runs through llmbuild.ResolveKeyGated: the
// PlatformKeyEntitlement wired via [SetKeyEntitlement] refuses it for an
// unentitled tenant in `open` Admission Mode (ADR-0054 seam (a), ADR-0055 —
// the former PHASE-B GAP, closed). A nil entitlement grants everything (the
// allowlist/self-host posture). A refusal is CodeFailedPrecondition with the
// seam's deliberately user-actionable message; every other failure keeps the
// RPC tier's redaction posture (logged detail, generic wire error).
func (s *VoiceServer) resolveComponentKey(ctx context.Context, tenantID uuid.UUID, component storage.Component) (string, error) {
	cfg, err := s.store.GetProviderConfigByComponent(ctx, tenantID, component)
	var cfgPtr *storage.ProviderConfig
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// No row: an env-fallback candidate (nil cfg).
	case err != nil:
		s.log.Error("resolveComponentKey: store read failed", "component", component, "err", err)
		return "", connect.NewError(connect.CodeInternal, errors.New("internal error"))
	case !isSaved(cfg.CredentialsLast4):
		// A row without a saved key ("" or the "env" placeholder) is the same
		// env-fallback candidate as no row. Normalize to nil: llmbuild treats
		// only "env" (not "") as unsaved and would try to decrypt an empty row.
	default:
		cfgPtr = &cfg
	}

	key, err := llmbuild.ResolveKeyGated(ctx, s.keyEnt, tenantID, s.cipher, cfgPtr, component)
	switch {
	case err == nil:
		return key, nil
	case errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
		return "", connect.NewError(connect.CodeFailedPrecondition, llmbuild.ErrNoPlatformKeyEntitlement)
	case cfgPtr != nil && s.cipher == nil:
		return "", connect.NewError(connect.CodeFailedPrecondition,
			errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
	default:
		// Decrypt or entitlement-read failure (the latter fails CLOSED): log
		// the detail, return the generic wire error.
		s.log.Error("resolveComponentKey: resolve failed", "component", component, "err", err)
		return "", connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// resolveDiscordToken resolves the deployment Bot token: no row / placeholder ->
// "" (which the live login rejects fast), a saved token -> decrypted plaintext.
func (s *VoiceServer) resolveDiscordToken(ctx context.Context, tenantID uuid.UUID) (string, error) {
	dep, err := s.store.GetDeploymentConfig(ctx, tenantID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		s.log.Error("resolveDiscordToken: store read failed", "err", err)
		return "", connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return s.openKey(dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext)
}

// openKey applies the hybrid decision to one (last4, ciphertext) pair.
func (s *VoiceServer) openKey(last4 string, ciphertext []byte) (string, error) {
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

// toProtoVoice maps a tts.Voice onto the wire Voice, building the "Provider ·
// Name" display label the select renders.
func toProtoVoice(v tts.Voice) *managementv1.Voice {
	return &managementv1.Voice{
		Provider: v.ProviderID,
		VoiceId:  v.VoiceID,
		Name:     v.Name,
		Language: v.Language,
		Label:    voiceLabel(v),
	}
}

// voiceLabel renders the "ElevenLabs · Marcus" display string. ElevenLabs is the
// only MVP TTS provider (ADR-0039); unknown providers fall back to their id.
func voiceLabel(v tts.Voice) string {
	name := v.Name
	if name == "" {
		name = v.VoiceID
	}
	provider := v.ProviderID
	if provider == elevenlabs.ProviderID {
		provider = "ElevenLabs"
	}
	if provider == "" {
		return name
	}
	return provider + " · " + name
}

// encodeWAV wraps signed-16-bit little-endian PCM in a canonical 44-byte
// RIFF/WAVE header so a browser <audio> element can play the preview without
// decoding raw PCM.
func encodeWAV(pcm []byte, sampleRate, channels int) []byte {
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	if channels <= 0 {
		channels = 1
	}
	const bitsPerSample = 16
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataLen := len(pcm)

	buf := make([]byte, 0, 44+dataLen)
	buf = append(buf, "RIFF"...)
	buf = appendU32(buf, uint32(36+dataLen)) // file size minus the 8-byte RIFF header
	buf = append(buf, "WAVE"...)
	// fmt subchunk (PCM).
	buf = append(buf, "fmt "...)
	buf = appendU32(buf, 16) // PCM fmt chunk size
	buf = appendU16(buf, 1)  // audio format: 1 = PCM
	buf = appendU16(buf, uint16(channels))
	buf = appendU32(buf, uint32(sampleRate))
	buf = appendU32(buf, uint32(byteRate))
	buf = appendU16(buf, uint16(blockAlign))
	buf = appendU16(buf, uint16(bitsPerSample))
	// data subchunk.
	buf = append(buf, "data"...)
	buf = appendU32(buf, uint32(dataLen))
	buf = append(buf, pcm...)
	return buf
}

func appendU16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func appendU32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// groqPingClient bounds the Groq health probe so an unreachable endpoint fails
// fast rather than hanging the whole health response.
var groqPingClient = &http.Client{Timeout: healthCheckTimeout}

// livePingGroq is the default Groq liveness test-call: a GET against the
// OpenAI-compatible /models endpoint with the bearer key. A 2xx means the key
// authenticates. An empty key falls back to GROQ_API_KEY (the hybrid env path).
func livePingGroq(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		apiKey = os.Getenv(groq.APIKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("groq: missing API key (set %s)", groq.APIKeyEnv)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, groq.DefaultBaseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("groq ping: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := groqPingClient.Do(req)
	if err != nil {
		return fmt.Errorf("groq ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("groq ping: HTTP %d", resp.StatusCode)
	}
	return nil
}

// geminiPingClient bounds the Gemini image-provider health probe (#311).
var geminiPingClient = &http.Client{Timeout: healthCheckTimeout}

// livePingGemini is the default Gemini image-provider liveness test-call (#311):
// a GET against the OpenAI-compatibility /models endpoint with the bearer key
// (mirrors livePingGroq — the same key family already serves Gemini LLM/embeddings
// through this surface). A 2xx means the key authenticates. An empty key falls
// back to GEMINI_API_KEY (the hybrid env path, ADR-0039).
func livePingGemini(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		apiKey = os.Getenv(gemini.APIKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("gemini: missing API key (set %s)", gemini.APIKeyEnv)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gemini.DefaultBaseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("gemini ping: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := geminiPingClient.Do(req)
	if err != nil {
		return fmt.Errorf("gemini ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gemini ping: HTTP %d", resp.StatusCode)
	}
	return nil
}
