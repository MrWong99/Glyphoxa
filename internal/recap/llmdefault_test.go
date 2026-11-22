package recap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

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

// A Tenant with no LLM Provider Config recaps on the deployment default: its
// adapter (so its key env), and its model (so a priced row).
func TestRecap_NoConfigRunsDeploymentDefault(t *testing.T) {
	st := newFakeStore()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "The Keeper."}
	sid := seedSession(st, uuid.New(), uuid.New(), "English", butler, time.Now(), sampleLines())

	var gotID, gotKey, gotModel string
	factory := func(providerID, apiKey string) (llm.Provider, error) {
		gotID, gotKey = providerID, apiKey
		return &stubProvider{text: "A recap.", capture: func(req llm.Request) { gotModel = req.Model }}, nil
	}
	eng := NewEngine(st, nil, billingtest.Ungated(), observe.Discard{}, nil,
		WithProviderFactory(factory), WithLLMDefault(haikuDefault(t)))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if gotID != "anthropic" || gotKey != "" {
		t.Errorf("provider = (%q, %q), want (anthropic, \"\") — the platform key", gotID, gotKey)
	}
	if gotModel != "claude-haiku-4-5" {
		t.Errorf("request model = %q, want claude-haiku-4-5", gotModel)
	}
}

// The platform-key entitlement gates the default provider's key like Groq's.
func TestRecap_DeploymentDefaultKeyIsEntitlementGated(t *testing.T) {
	st := newFakeStore()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "The Keeper."}
	sid := seedSession(st, uuid.New(), uuid.New(), "English", butler, time.Now(), sampleLines())
	eng := NewEngine(st, nil, billingtest.Ungated(), observe.Discard{}, nil,
		WithProviderFactory(func(_, _ string) (llm.Provider, error) {
			t.Fatal("provider must not be built when the entitlement refuses")
			return nil, nil
		}),
		WithKeyEntitlement(&refusingEntitlement{}), WithLLMDefault(haikuDefault(t)))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
		t.Fatalf("err = %v, want ErrNoPlatformKeyEntitlement", err)
	}
}
