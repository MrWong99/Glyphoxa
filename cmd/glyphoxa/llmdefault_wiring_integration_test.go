//go:build integration

package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// wiringDefault is a deployment whose default LLM is Claude Haiku 4.5.
func wiringDefault(t *testing.T) llmbuild.DeploymentDefault {
	t.Helper()
	d, err := llmbuild.DeploymentDefaultFromEnv(func(k string) string {
		return map[string]string{
			llmbuild.EnvDefaultProvider: "anthropic",
			llmbuild.EnvDefaultModel:    "claude-haiku-4-5",
			"ANTHROPIC_API_KEY":         "boot-check-only",
		}[k]
	})
	if err != nil {
		t.Fatalf("DeploymentDefaultFromEnv: %v", err)
	}
	return d
}

// TestManagementMounts_DeploymentDefaultReachesTheRPCs pins the composition
// root (#702 review): the deployment-default LLM handed to managementMounts
// must reach BOTH RPC servers that answer for it — the Configuration read's
// effective_llm (ProviderService) and the model catalog's pinned default
// (VoiceService). Each server is covered on its own; what is covered here is
// that runWeb's mount table actually hands the value over, on the REAL mount
// table behind the real auth stack. Dropping either SetLLMDefault call leaves
// every other test green while the screen names Groq on an Anthropic default.
func TestManagementMounts_DeploymentDefaultReachesTheRPCs(t *testing.T) {
	// No platform key in THIS process: the catalog must not reach out.
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := bootMigratedStore(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	sessions := session.NewIntentControl(store, log, session.IntentControlConfig{})
	// Requests below arrive through the dev middleware as the synthetic dev
	// operator, so the mount table is built as runWeb builds it on a dev
	// instance: Dev makes that operator a Platform Admin, which is what lets
	// their own Tenant resolve as a workspace on an allowlist deployment
	// (ADR-0065).
	mounts := managementMounts(store, nil, nil, nil, log,
		sessions, nil, nil, nil, nil, nil, nil, nil, nil, "", auth.AdmissionAllowlist, auth.PlatformAdmins{Dev: true}, "", nil,
		newOffSessionMetering(auth.AdmissionAllowlist, store, log), wiringDefault(t))
	mux := http.NewServeMux()
	for _, m := range mounts {
		mux.Handle(m.Path, m.Handler)
	}
	d := &devSessions{store: store, authn: store, now: time.Now}
	srv := httptest.NewServer(devAuthMiddleware(d, log, mux))
	t.Cleanup(srv.Close)

	providers := managementv1connect.NewProviderServiceClient(http.DefaultClient, srv.URL+"/api", connect.WithProtoJSON())
	list, err := providers.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("ListProviderConfigs: %v", err)
	}
	if eff := list.Msg.GetEffectiveLlm(); eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-4-5" || !eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want the deployment default anthropic / claude-haiku-4-5", eff)
	}

	voice := managementv1connect.NewVoiceServiceClient(http.DefaultClient, srv.URL+"/api", connect.WithProtoJSON())
	models, err := voice.ListModels(ctx, connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := models.Msg.GetModels(); len(got) == 0 || got[0] != "claude-haiku-4-5" {
		t.Errorf("anthropic catalog = %v, want the deployment default model pinned first", got)
	}

	// The Planning chat read (ADR-0062 amendment 2026-11-22): a row-less
	// Tenant's chat answers with the deployment default too. The engine half of
	// this pair is TestPlanningChatEngine_RowlessTenantRunsTheDeploymentDefault.
	chat := list.Msg.GetEffectiveChatLlm()
	if chat.GetProvider() != "anthropic" || chat.GetModel() != "claude-haiku-4-5" ||
		chat.GetSource() != managementv1.ChatLlmSource_CHAT_LLM_SOURCE_PLATFORM_DEFAULT {
		t.Errorf("effective_chat_llm = %+v, want the deployment default anthropic / claude-haiku-4-5", chat)
	}
	// No ANTHROPIC_API_KEY in this process: the read must say no key is
	// reachable instead of naming a model as if an exchange would work.
	if chat.GetKeySource() != managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_NONE {
		t.Errorf("effective_chat_llm.key_source = %v with no platform key in the process, want NONE", chat.GetKeySource())
	}
}

// TestPlanningChatEngine_RowlessTenantRunsTheDeploymentDefault pins the chat
// half of the composition root (#712 item 4, for chat; assist and the voice
// session Config are pinned without Postgres in llmdefault_wiring_test.go and
// boot_test.go): the engine the
// ChatService serves — built by newPlanningChatEngine, the function
// managementMounts calls — resolves a row-less Tenant to the deployment
// default, the provider and model effective_chat_llm names for it (asserted on
// the real mount table above). It is checked twice: on the engine's own
// resolution, and through a whole exchange, which with no key in the process
// stops at the Anthropic adapter's missing-key error before any network.
// Dropping planchat.WithLLMDefault makes both Groq's.
func TestPlanningChatEngine_RowlessTenantRunsTheDeploymentDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	store := bootMigratedStore(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	d := wiringDefault(t)

	tenantID, err := store.CreateTenant(ctx, "Wiring Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := store.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Wiring", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	campaign, err := store.GetCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	butler, err := store.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	thread, err := store.CreatePlanningThread(ctx, campaignID, "")
	if err != nil {
		t.Fatalf("CreatePlanningThread: %v", err)
	}

	eng := newPlanningChatEngine(store, nil, search.New(store, log), nil,
		newOffSessionMetering(auth.AdmissionAllowlist, store, log), observe.NewPrometheusRecorder(), log, nil, d)

	sel, err := eng.ResolveChatLLM(ctx, tenantID, butler)
	if err != nil {
		t.Fatalf("ResolveChatLLM: %v", err)
	}
	wantProvider, wantModel := d.Select(nil)
	if sel.Provider != wantProvider || sel.Model != wantModel || sel.Source != llmbuild.ChatSourceDefault {
		t.Errorf("the chat engine resolves %s/%s (%s), want the deployment default %s/%s",
			sel.Provider, sel.Model, sel.Source, wantProvider, wantModel)
	}

	_, err = eng.Exchange(ctx, tenantID, campaign, thread.ID, "Plan the next session.", discardChatEvents{})
	if err == nil {
		t.Fatal("Exchange succeeded with no LLM key in the process")
	}
	if msg := err.Error(); !strings.Contains(msg, "anthropic") || strings.Contains(msg, "groq") {
		t.Errorf("Exchange err = %v, want the Anthropic adapter's missing-key refusal (the deployment default)", err)
	}
}

// discardChatEvents drops an exchange's stream.
type discardChatEvents struct{}

func (discardChatEvents) Text(string) error { return nil }
func (discardChatEvents) Tool(string, bool) {}

// TestBuildVoiceDeps_RecapRunsTheDeploymentDefault pins the other half of the
// composition root: the recap Engine buildVoiceDeps constructs — the one the
// slash command, the GenerateRecap RPC and the recap Tool share — resolves a
// row-less Tenant to the deployment default. With no key in the process the
// call stops at the Anthropic adapter's missing-key error, before any network:
// which adapter refused is the observable. Dropping recap.WithLLMDefault makes
// it Groq's.
func TestBuildVoiceDeps_RecapRunsTheDeploymentDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	store := bootMigratedStore(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	tenantID, err := store.CreateTenant(ctx, "Wiring Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := store.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Wiring", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	vs, err := store.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	if err := store.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: uuid.NewString(), Seq: 1,
		Who: "GM", Kind: "line", TS: time.Now(), Text: "You enter the tavern.",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine: %v", err)
	}

	vd := buildVoiceDeps(store, nil, observe.NewPrometheusRecorder(), log, nil, auth.AdmissionAllowlist,
		voiceevent.NewBus(), session.NewRegistry(), nil, nil, nil, false, wiringDefault(t))
	_, err = vd.recapEngine.Recap(ctx, []uuid.UUID{vs.ID})
	if err == nil {
		t.Fatal("Recap succeeded with no LLM key in the process")
	}
	if msg := err.Error(); !strings.Contains(msg, "anthropic") || strings.Contains(msg, "groq") {
		t.Errorf("Recap err = %v, want the Anthropic adapter's missing-key refusal (the deployment default)", err)
	}
}
