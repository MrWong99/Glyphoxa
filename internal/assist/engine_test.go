package assist

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// TestGeneratePersonaHappyPath: the draft comes back trimmed and de-fenced, the
// campaign/system/language and the agent name/title all season the prompt, and
// nothing but the model text is returned.
func TestGeneratePersonaHappyPath(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "German")

	var req llm.Request
	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "```\nYou are Bart, a gruff innkeeper.\n```", capture: func(r llm.Request) { req = r }}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))

	persona, err := eng.GeneratePersona(context.Background(), c, PersonaInput{
		AgentName:  "Bart",
		AgentTitle: "Innkeeper",
		Prompt:     "a gruff innkeeper",
	})
	if err != nil {
		t.Fatalf("GeneratePersona: %v", err)
	}
	if persona != "You are Bart, a gruff innkeeper." {
		t.Errorf("persona = %q, want the de-fenced trimmed draft", persona)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want system+user", len(req.Messages))
	}
	sys, user := req.Messages[0].Text, req.Messages[1].Text
	for _, want := range []string{"Test Campaign", "D&D 5e", "Write in German."} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	for _, want := range []string{"a gruff innkeeper", "Bart", "Innkeeper"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
}

// TestGeneratePersonaUnusable: an empty (whitespace-only) model reply is
// ErrUnusableResponse, never an empty persona.
func TestGeneratePersonaUnusable(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "  \n"}, nil
	}))
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); !errors.Is(err, ErrUnusableResponse) {
		t.Fatalf("err = %v, want ErrUnusableResponse", err)
	}
}

// TestGeneratePersonaMetersUsage: provider-reported usage is metered on the
// default (Groq) path, priced on groq.DefaultModel — the recap posture.
func TestGeneratePersonaMetersUsage(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	rec := &capRec{}
	eng := NewEngine(st, nil, rec, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "You are someone.", usage: &llm.Usage{InputTokens: 42, OutputTokens: 7}}, nil
	}))
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); err != nil {
		t.Fatalf("GeneratePersona: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("LLMTokens calls = %d, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.in != 42 || got.out != 7 {
		t.Errorf("tokens = (%d,%d), want reported (42,7)", got.in, got.out)
	}
	if got.provider != observe.ProviderGroq || got.model != groq.DefaultModel {
		t.Errorf("priced (%s,%s), want (groq, %s)", got.provider, got.model, groq.DefaultModel)
	}
}

// TestGenerateKnowledgeHappyPath: the JSON draft parses into validated nodes and
// edges, and public existing entry names season the prompt while gm_private
// entries never do.
func TestGenerateKnowledgeHappyPath(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "German")
	st.nodes[c.ID] = []storage.KGNode{
		{Name: "Saltmarsh", Type: storage.KGNodeLocation},
		{Name: "The Hidden Twist", Type: storage.KGNodeNote, GMPrivate: true},
	}

	var req llm.Request
	draftJSON := `{"nodes":[
		{"type":"npc","name":"Wilhelmine","body":"A fence.","gm_private":false},
		{"type":"faction","name":"The Grey Hands","body":"Thieves.","gm_private":true}
	],"edges":[{"from":0,"to":1,"type":"member_of"}]}`
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: draftJSON, capture: func(r llm.Request) { req = r }}, nil
	}))

	d, err := eng.GenerateKnowledge(context.Background(), c, "the thieves' guild")
	if err != nil {
		t.Fatalf("GenerateKnowledge: %v", err)
	}
	if len(d.Nodes) != 2 || len(d.Edges) != 1 {
		t.Fatalf("draft = %d nodes / %d edges, want 2/1", len(d.Nodes), len(d.Edges))
	}
	if !d.Nodes[1].GMPrivate {
		t.Errorf("gm_private flag lost on node 1")
	}
	sys := req.Messages[0].Text
	if !strings.Contains(sys, "Saltmarsh") {
		t.Errorf("system prompt missing the public existing entry name")
	}
	if strings.Contains(sys, "The Hidden Twist") {
		t.Errorf("system prompt leaks a gm_private entry name")
	}
	if !strings.Contains(sys, "Write in German.") {
		t.Errorf("system prompt missing the language pin")
	}
	if req.Messages[1].Text != "the thieves' guild" {
		t.Errorf("user prompt = %q, want the GM prompt verbatim", req.Messages[1].Text)
	}
}

// TestGenerateKnowledgeUnusable: model prose with no JSON maps onto
// ErrUnusableResponse (the RPC layer turns that into CodeUnavailable).
func TestGenerateKnowledgeUnusable(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "Sorry, I'd rather not."}, nil
	}))
	if _, err := eng.GenerateKnowledge(context.Background(), c, "x"); !errors.Is(err, ErrUnusableResponse) {
		t.Fatalf("err = %v, want ErrUnusableResponse", err)
	}
}

// TestGenerateStreamErrorStillMetersReported: a mid-stream failure surfaces the
// error but still meters the usage the provider reported (ADR-0045 error rule).
func TestGenerateStreamErrorStillMetersReported(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	rec := &capRec{}
	eng := NewEngine(st, nil, rec, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial", usage: &llm.Usage{InputTokens: 5, OutputTokens: 2}, errText: "boom"}, nil
	}))
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); err == nil {
		t.Fatal("err = nil, want the stream error")
	}
	if len(rec.calls) != 1 || rec.calls[0].in != 5 || rec.calls[0].out != 2 {
		t.Fatalf("metered %+v, want the reported (5,2)", rec.calls)
	}
}

// TestRefusedEntitlementBlocksEnvFallback: with no provider config and a
// refusing entitlement, the env-key fallback is refused (ADR-0054/0055) — the
// same posture the recap engine holds.
func TestRefusedEntitlementBlocksEnvFallback(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	eng := NewEngine(st, nil, observe.Discard{}, nil,
		WithProviderFactory(func(_, _ string) (llm.Provider, error) {
			t.Fatal("provider must not be built when the key resolution is refused")
			return nil, nil
		}),
		WithKeyEntitlement(refuseEntitlement{}),
	)
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
		t.Fatalf("err = %v, want ErrNoPlatformKeyEntitlement", err)
	}
}

// refuseEntitlement denies every tenant the platform-key env fallback.
type refuseEntitlement struct{}

func (refuseEntitlement) PlatformKeyAllowed(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
