//go:build live && integration

// Live proof of the deployment-default LLM (#702), end to end minus Discord: a
// real Postgres holding a Tenant with NO provider_config row, the real boot read
// of GLYPHOXA_LLM_DEFAULT_PROVIDER / _MODEL, the real session resolution, the
// real adapter and one streamed reply from the real API. It makes PAID calls,
// so it hides behind `live` (and `integration`, for the testcontainers DB):
//
//	ANTHROPIC_API_KEY=… GROQ_API_KEY=… \
//	  go test -tags 'live integration' -run TestLive_DeploymentDefaultLLM ./internal/wirenpc/
package wirenpc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
)

// liveSession resolves a Voice Session for campaignID exactly as RunFromDB does
// (roster load → key resolution → session LLM), from the process environment,
// and returns the resolved Config plus the adapter buildConversation would get.
func liveSession(t *testing.T, st *storage.Store, campaignID [16]byte) (Config, llm.Provider) {
	t.Helper()
	ctx := context.Background()
	d, err := llmbuild.DeploymentDefaultFromEnv(os.Getenv) // the boot read
	if err != nil {
		t.Fatalf("DeploymentDefaultFromEnv: %v", err)
	}
	cfg := Config{CampaignID: campaignID, LLMDefault: d}
	npcs, primary, campaign, err := loadCampaignNPCs(ctx, st, cfg.CampaignID, cfg.LLMDefault)
	if err != nil {
		t.Fatalf("loadCampaignNPCs: %v", err)
	}
	keys, llmCfg, err := resolveSessionKeys(ctx, st, campaign.TenantID, primary, nil, nil)
	if err != nil {
		t.Fatalf("resolveSessionKeys: %v", err)
	}
	if llmCfg != nil {
		t.Fatalf("the Tenant has an LLM provider_config row (%+v); this test needs none", llmCfg)
	}
	cfg.npcs, cfg.keys, cfg.language = npcs, keys, campaign.Language
	cfg = cfg.withSessionLLM(llmCfg)
	provider, err := newLLM(cfg.llmProviderID, cfg.keys.llm)
	if err != nil {
		t.Fatalf("newLLM(%q): %v", cfg.llmProviderID, err)
	}
	return cfg, provider
}

// streamOneReply drives one streamed Agent turn through the engine the session
// would build for its first NPC and returns the text, the number of deltas it
// arrived in, and what the spend meter was told.
func streamOneReply(t *testing.T, cfg Config, provider llm.Provider) (text string, deltas int, rec *labelCapturingRecorder) {
	t.Helper()
	rec = &labelCapturingRecorder{}
	eng := engineFactory(provider, tool.NewRegistry(), cfg.language, rec, llmProviderLabel(cfg.llmProviderID), retry.Policy{})(cfg.npcs[0])
	streaming, ok := eng.(agent.StreamingEngine)
	if !ok {
		t.Fatalf("engine %T does not stream", eng)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	text, err := streaming.GenerateStream(ctx, []llm.Message{
		{Role: llm.RoleSystem, Text: "Du bist Flo, Wirtin einer Taverne. Antworte in einem kurzen deutschen Satz."},
		{Role: llm.RoleUser, Text: "Guten Abend! Was gibt es heute zu essen?"},
	}, func(string) error { deltas++; return nil })
	if err != nil {
		t.Fatalf("GenerateStream on %s/%s: %v", cfg.llmProviderID, cfg.npcs[0].model, err)
	}
	return text, deltas, rec
}

func TestLive_DeploymentDefaultLLM(t *testing.T) {
	if os.Getenv(anthropic.APIKeyEnv) == "" {
		t.Skipf("%s not set", anthropic.APIKeyEnv)
	}
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Live LLM default "+t.Name())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Saltmarsh", System: "dnd5e", Language: "de"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := st.CreateAgent(ctx, storage.NewAgent{CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Flo", Persona: "Wirtin."}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	t.Run("anthropic default builds an Anthropic engine and streams a reply", func(t *testing.T) {
		t.Setenv(llmbuild.EnvDefaultProvider, "anthropic")
		t.Setenv(llmbuild.EnvDefaultModel, "claude-haiku-4-5")

		cfg, provider := liveSession(t, st, campaignID)
		if cfg.llmProviderID != "anthropic" {
			t.Fatalf("session provider = %q, want anthropic", cfg.llmProviderID)
		}
		if _, ok := provider.(*anthropic.Client); !ok {
			t.Fatalf("session adapter = %T, want *anthropic.Client", provider)
		}
		for _, npc := range cfg.npcs {
			if npc.model != "claude-haiku-4-5" {
				t.Errorf("%s model = %q, want claude-haiku-4-5", npc.name, npc.model)
			}
		}
		if cfg.keys.llm != "" {
			t.Error("a row-less Tenant resolved a non-empty LLM key; want the platform env key")
		}

		text, deltas, rec := streamOneReply(t, cfg, provider)
		if strings.TrimSpace(text) == "" || deltas == 0 {
			t.Fatalf("reply = %q in %d deltas, want a streamed non-empty reply", text, deltas)
		}
		if rec.tokensProvider != observe.ProviderAnthropic || rec.tokensModel != "claude-haiku-4-5" {
			t.Errorf("metered as (%s, %s), want (anthropic, claude-haiku-4-5)", rec.tokensProvider, rec.tokensModel)
		}
		t.Logf("anthropic/claude-haiku-4-5 streamed %d deltas: %q", deltas, text)
	})

	t.Run("unsetting the envs restores Groq", func(t *testing.T) {
		t.Setenv(llmbuild.EnvDefaultProvider, "")
		t.Setenv(llmbuild.EnvDefaultModel, "")

		cfg, provider := liveSession(t, st, campaignID)
		if cfg.llmProviderID != "groq" {
			t.Fatalf("session provider = %q, want groq", cfg.llmProviderID)
		}
		if _, ok := provider.(*openaicompat.Client); !ok {
			t.Fatalf("session adapter = %T, want the Groq *openaicompat.Client", provider)
		}
		if got := cfg.npcs[0].model; got != "openai/gpt-oss-120b" {
			t.Errorf("model = %q, want openai/gpt-oss-120b", got)
		}
		if os.Getenv("GROQ_API_KEY") == "" {
			t.Log("GROQ_API_KEY not set: resolution proven, live Groq reply skipped")
			return
		}
		text, deltas, rec := streamOneReply(t, cfg, provider)
		if strings.TrimSpace(text) == "" || deltas == 0 {
			t.Fatalf("reply = %q in %d deltas, want a streamed non-empty reply", text, deltas)
		}
		if rec.tokensProvider != observe.ProviderGroq || rec.tokensModel != "openai/gpt-oss-120b" {
			t.Errorf("metered as (%s, %s), want (groq, openai/gpt-oss-120b)", rec.tokensProvider, rec.tokensModel)
		}
		t.Logf("groq/openai/gpt-oss-120b streamed %d deltas: %q", deltas, text)
	})
}
