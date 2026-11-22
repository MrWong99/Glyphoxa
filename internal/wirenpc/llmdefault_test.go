package wirenpc

import (
	"log/slog"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// haikuDefault is a deployment whose default voice LLM is Claude Haiku 4.5
// (GLYPHOXA_LLM_DEFAULT_PROVIDER / _MODEL, #702).
func haikuDefault(t *testing.T) llmbuild.DeploymentDefault {
	t.Helper()
	d, err := llmbuild.DeploymentDefaultFromEnv(func(k string) string {
		return map[string]string{
			llmbuild.EnvDefaultProvider: "anthropic",
			llmbuild.EnvDefaultModel:    "claude-haiku-4-5",
			"ANTHROPIC_API_KEY":         "test-key",
		}[k]
	})
	if err != nil {
		t.Fatalf("DeploymentDefaultFromEnv: %v", err)
	}
	return d
}

// A session whose Tenant saved no LLM Provider Config runs the deployment
// default: its adapter, its model, its price label.
func TestSessionLLM_NoRowRunsDeploymentDefault(t *testing.T) {
	cfg := Config{LLMDefault: haikuDefault(t), npcs: []npcSpec{{name: "Flo"}, {name: "Butler"}}}
	cfg = cfg.withSessionLLM(nil)

	if cfg.llmProviderID != "anthropic" {
		t.Errorf("llmProviderID = %q, want anthropic", cfg.llmProviderID)
	}
	for _, npc := range cfg.npcs {
		if npc.model != "claude-haiku-4-5" {
			t.Errorf("%s model = %q, want claude-haiku-4-5", npc.name, npc.model)
		}
	}
	if got := llmProviderLabel(cfg.llmProviderID); got != observe.ProviderAnthropic {
		t.Errorf("metric/price label = %q, want anthropic", got)
	}
}

// With neither env set the session is exactly what it was: Groq, gpt-oss-120b.
func TestSessionLLM_UnsetEnvsStayGroq(t *testing.T) {
	cfg := Config{npcs: []npcSpec{{name: "Flo"}}}
	cfg = cfg.withSessionLLM(nil)
	if cfg.llmProviderID != groq.ProviderID {
		t.Errorf("llmProviderID = %q, want groq", cfg.llmProviderID)
	}
	if got := cfg.npcs[0].model; got != groq.DefaultModel {
		t.Errorf("model = %q, want %q", got, groq.DefaultModel)
	}
}

// The Tenant's (or the primary Agent's) own row wins over the deployment default.
func TestSessionLLM_TenantRowWins(t *testing.T) {
	cfg := Config{LLMDefault: haikuDefault(t), npcs: []npcSpec{{name: "Flo"}, {name: "Sly", model: "openai/gpt-oss-20b"}}}
	cfg = cfg.withSessionLLM(&storage.ProviderConfig{Provider: "groq"})
	if cfg.llmProviderID != "groq" {
		t.Errorf("llmProviderID = %q, want the row's groq", cfg.llmProviderID)
	}
	if got := cfg.npcs[0].model; got != groq.DefaultModel {
		t.Errorf("model-less NPC on a groq row = %q, want groq's default (never the Anthropic deployment model)", got)
	}
	if got := cfg.npcs[1].model; got != "openai/gpt-oss-20b" {
		t.Errorf("explicit model = %q, want it untouched", got)
	}
}

// withSessionLLM must not write through to the caller's roster slice.
func TestSessionLLM_DoesNotMutateCallerRoster(t *testing.T) {
	npcs := []npcSpec{{name: "Flo"}}
	_ = Config{npcs: npcs}.withSessionLLM(nil)
	if npcs[0].model != "" {
		t.Errorf("caller's spec was mutated: model = %q", npcs[0].model)
	}
}

// The Highlight classifier rides the session's provider: with no row it must
// build the deployment default's adapter, not Groq.
func TestHighlightClassifier_NoRowBuildsDeploymentDefault(t *testing.T) {
	orig := newLLM
	t.Cleanup(func() { newLLM = orig })
	var gotProvider string
	newLLM = func(providerID, _ string) (llm.Provider, error) {
		gotProvider = providerID
		return stubLLM{}, nil
	}
	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	cfg := Config{
		Tape: tp, Highlights: &fakeHighlightSink{}, StageMetrics: &labelSpy{},
		LLMDefault: haikuDefault(t), npcs: []npcSpec{{name: "Flo"}},
	}.withSessionLLM(nil)
	d := buildHighlightDetector(cfg, voiceevent.NewBus(), slog.New(slog.DiscardHandler))
	if d == nil {
		t.Fatal("detector = nil, want non-nil")
	}
	defer d.Close()
	if gotProvider != "anthropic" {
		t.Errorf("classifier provider = %q, want anthropic", gotProvider)
	}
}

// The model the loader fills into a model-less spec follows the deployment
// default when no row names a provider.
func TestDefaultEmptyModels_NoProviderFollowsDeploymentDefault(t *testing.T) {
	specs := []npcSpec{{name: "a"}, {name: "b", model: "explicit"}}
	defaultEmptyModels(specs, "", haikuDefault(t))
	if specs[0].model != "claude-haiku-4-5" {
		t.Errorf("empty model = %q, want claude-haiku-4-5", specs[0].model)
	}
	if specs[1].model != "explicit" {
		t.Errorf("explicit model = %q, want it untouched", specs[1].model)
	}
}

// One adapter serves the whole Roster, so an NPC's model may only come from a
// Provider Config of THAT adapter's provider (#702 review): a row on another
// provider — the Tenant's newest row while the primary Agent is bound
// elsewhere, or an Agent bound to another provider's row — would hand, say, a
// Claude model id to the Groq adapter, which fails every turn of that NPC.
func TestResolveNPCModel_OnlyFromTheSessionProvider(t *testing.T) {
	t.Parallel()
	groqRow := &storage.ProviderConfig{Provider: "groq", Model: "openai/gpt-oss-20b"}
	claudeRow := &storage.ProviderConfig{Provider: "anthropic", Model: "claude-haiku-4-5"}
	for name, tc := range map[string]struct {
		bound, tenant *storage.ProviderConfig
		session, want string
	}{
		"bound row of the session provider wins":         {groqRow, claudeRow, "groq", "openai/gpt-oss-20b"},
		"tenant row of the session provider is fallback": {nil, groqRow, "groq", "openai/gpt-oss-20b"},
		"tenant row on another provider is ignored":      {nil, claudeRow, "groq", ""},
		"bound row on another provider is ignored":       {claudeRow, groqRow, "groq", "openai/gpt-oss-20b"},
		"neither belongs to the session":                 {claudeRow, claudeRow, "groq", ""},
		"model-less bound row falls to the tenant row":   {&storage.ProviderConfig{Provider: "groq"}, groqRow, "groq", "openai/gpt-oss-20b"},
		"no rows": {nil, nil, "groq", ""},
	} {
		if got := resolveNPCModel(tc.bound, tc.tenant, tc.session); got != tc.want {
			t.Errorf("%s: model = %q, want %q", name, got, tc.want)
		}
	}
}
