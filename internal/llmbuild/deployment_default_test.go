package llmbuild

import (
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestDeploymentDefault_UnsetIsGroqCodeDefault(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(nil))
	if err != nil {
		t.Fatalf("unset envs must boot: %v", err)
	}
	if d != (DeploymentDefault{}) {
		t.Fatalf("unset envs must yield the zero value, got %+v", d)
	}
	if got := d.Provider(); got != "groq" {
		t.Errorf("Provider() = %q, want groq", got)
	}
	if got := d.VoiceModel(d.Provider(), ""); got != "openai/gpt-oss-120b" {
		t.Errorf("VoiceModel = %q, want openai/gpt-oss-120b", got)
	}
	p, m := d.Select(nil)
	if p != "groq" || m != "openai/gpt-oss-120b" {
		t.Errorf("Select(nil) = (%q, %q), want (groq, openai/gpt-oss-120b) — the code default, named", p, m)
	}
	// One model default per config (#702 review): a model-less row on a
	// provider that is NOT the deployment default still resolves to a model —
	// the one a Voice Session runs — never to "" for an adapter to fill.
	if p, m := d.Select(&storage.ProviderConfig{Provider: "anthropic"}); p != "anthropic" || m != "claude-haiku-5-5" {
		t.Errorf("Select(model-less anthropic row) = (%q, %q), want (anthropic, claude-haiku-5-5)", p, m)
	}
}

func TestDeploymentDefault_AnthropicDefault(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(map[string]string{
		EnvDefaultProvider:  "anthropic",
		EnvDefaultModel:     "claude-haiku-4-5",
		"ANTHROPIC_API_KEY": "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p, m := d.Select(nil); p != "anthropic" || m != "claude-haiku-4-5" {
		t.Errorf("Select(nil) = (%q, %q)", p, m)
	}
	if got := d.VoiceModel("anthropic", ""); got != "claude-haiku-4-5" {
		t.Errorf("VoiceModel(anthropic) = %q", got)
	}
}

func TestDeploymentDefault_TenantRowWins(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(map[string]string{
		EnvDefaultProvider:  "anthropic",
		EnvDefaultModel:     "claude-haiku-4-5",
		"ANTHROPIC_API_KEY": "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	row := &storage.ProviderConfig{Provider: "groq", Model: "openai/gpt-oss-20b"}
	if p, m := d.Select(row); p != "groq" || m != "openai/gpt-oss-20b" {
		t.Errorf("Select(row) = (%q, %q), want the row", p, m)
	}
	// A row on ANOTHER provider with no model gets that provider's own
	// default, never the deployment model (it belongs to a different API).
	if got := d.VoiceModel("groq", ""); got != "openai/gpt-oss-120b" {
		t.Errorf("VoiceModel(groq, \"\") = %q", got)
	}
	// A row on the default provider with no model follows the deployment model.
	if p, m := d.Select(&storage.ProviderConfig{Provider: "anthropic"}); p != "anthropic" || m != "claude-haiku-4-5" {
		t.Errorf("Select(model-less anthropic row) = (%q, %q)", p, m)
	}
	// An explicit model always wins.
	if got := d.VoiceModel("anthropic", "claude-haiku-5-5"); got != "claude-haiku-5-5" {
		t.Errorf("explicit model lost: %q", got)
	}
}

func TestDeploymentDefault_ProviderOnlyUsesVoiceDefaultModel(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(map[string]string{
		EnvDefaultProvider:  "anthropic",
		"ANTHROPIC_API_KEY": "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.VoiceModel("anthropic", ""); got != "claude-haiku-5-5" {
		t.Errorf("VoiceModel = %q, want the Haiku voice default", got)
	}
}

func TestDeploymentDefault_ModelOnlyAppliesToGroq(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(map[string]string{EnvDefaultModel: "openai/gpt-oss-20b"}))
	if err != nil {
		t.Fatal(err)
	}
	if p, m := d.Select(nil); p != "groq" || m != "openai/gpt-oss-20b" {
		t.Errorf("Select(nil) = (%q, %q)", p, m)
	}
}

func TestDeploymentDefault_FailsFast(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"unknown provider", map[string]string{EnvDefaultProvider: "openai"},
			[]string{EnvDefaultProvider, `"openai"`, "groq", "anthropic", "gemini"}},
		{"missing platform key", map[string]string{EnvDefaultProvider: "anthropic"},
			[]string{EnvDefaultProvider, "ANTHROPIC_API_KEY"}},
		{"groq named but keyless", map[string]string{EnvDefaultProvider: "groq"},
			[]string{"GROQ_API_KEY"}},
		{"gemini named but keyless", map[string]string{EnvDefaultProvider: "gemini"},
			[]string{"GEMINI_API_KEY"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeploymentDefaultFromEnv(env(tc.env))
			if err == nil {
				t.Fatal("want a boot error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestDeploymentDefault_NormalizesCaseAndSpace(t *testing.T) {
	d, err := DeploymentDefaultFromEnv(env(map[string]string{
		EnvDefaultProvider: " Anthropic ", EnvDefaultModel: " claude-haiku-4-5 ", "ANTHROPIC_API_KEY": "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p, m := d.Select(nil); p != "anthropic" || m != "claude-haiku-4-5" {
		t.Errorf("Select(nil) = (%q, %q)", p, m)
	}
}

// Every model this package can hand out WITHOUT anyone naming it — a provider's
// default, its voice default, and a deployment default set to a known id — must
// have a price row. A miss is charged at the conservative fallback ($5 / $15 per
// MTok), dozens of times the real rate, which drains a trial allowance (#702).
func TestDefaultModelsArePriced(t *testing.T) {
	for provider := range platformKeyEnv {
		for _, model := range []string{DefaultVoiceModel(provider)} {
			if !spend.LLMPriced(observe.Provider(provider), model) {
				t.Errorf("default model (%s, %s) has no price row in internal/spend/prices.go", provider, model)
			}
		}
	}
	for _, known := range []struct{ provider, model, keyEnv string }{
		{"groq", "openai/gpt-oss-120b", "GROQ_API_KEY"},
		{"anthropic", "claude-haiku-4-5", "ANTHROPIC_API_KEY"},
		{"anthropic", "claude-haiku-5-5", "ANTHROPIC_API_KEY"},
	} {
		d, err := DeploymentDefaultFromEnv(env(map[string]string{
			EnvDefaultProvider: known.provider, EnvDefaultModel: known.model, known.keyEnv: "k",
		}))
		if err != nil {
			t.Fatal(err)
		}
		p, m := d.Select(nil)
		if !spend.LLMPriced(observe.Provider(p), d.VoiceModel(p, m)) {
			t.Errorf("deployment default (%s, %s) is unpriced", p, m)
		}
		if !d.Priced() {
			t.Errorf("Priced() = false for (%s, %s)", p, m)
		}
	}
}
