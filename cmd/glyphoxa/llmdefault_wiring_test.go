package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/assist"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// rowlessAssistStore is an assist.Store for a Tenant with no LLM Provider
// Config at all: a Butler with no config of its own, no Tenant-level row, an
// empty Knowledge Graph.
type rowlessAssistStore struct{}

func (rowlessAssistStore) GetButler(context.Context, uuid.UUID) (storage.Agent, error) {
	return storage.Agent{}, nil
}

func (rowlessAssistStore) GetProviderConfig(context.Context, uuid.UUID) (storage.ProviderConfig, error) {
	return storage.ProviderConfig{}, storage.ErrNotFound
}

func (rowlessAssistStore) GetProviderConfigByComponent(context.Context, uuid.UUID, storage.Component) (storage.ProviderConfig, error) {
	return storage.ProviderConfig{}, storage.ErrNotFound
}

func (rowlessAssistStore) ListNodes(context.Context, uuid.UUID) ([]storage.KGNode, error) {
	return nil, nil
}

// TestNewAssistEngine_RowlessTenantRunsTheDeploymentDefault pins the assist
// half of the composition root (#712 item 4): the Engine runWeb serves — built
// by newAssistEngine — resolves a row-less Tenant to the deployment default.
// With no key in the process the draft stops at the Anthropic adapter's
// missing-key error, before any network: which adapter refused is the
// observable. Dropping assist.WithLLMDefault makes it Groq's.
func TestNewAssistEngine_RowlessTenantRunsTheDeploymentDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	// The boot check wants a platform key for the named provider; the process
	// the engine then runs in has none.
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

	eng := newAssistEngine(rowlessAssistStore{}, nil, billingtest.Ungated(), observe.NewPrometheusRecorder(),
		slog.New(slog.DiscardHandler), nil, d)
	campaign := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Wiring", System: "dnd5e", Language: "en"}
	_, err = eng.GeneratePersona(context.Background(), campaign, assist.PersonaInput{Prompt: "x"})
	if err == nil {
		t.Fatal("GeneratePersona succeeded with no LLM key in the process")
	}
	if msg := err.Error(); !strings.Contains(msg, "anthropic") || strings.Contains(msg, "groq") {
		t.Errorf("GeneratePersona err = %v, want the Anthropic adapter's missing-key refusal (the deployment default)", err)
	}
}
