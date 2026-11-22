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
	mounts := managementMounts(store, nil, nil, nil, log,
		sessions, nil, nil, nil, nil, nil, nil, nil, nil, "", auth.AdmissionAllowlist, "", nil,
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
}

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
