package recap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// seedSession adds a campaign (once per campaignID), a Butler, a session, and its
// lines to the store. Returns the session id.
func seedSession(s *fakeStore, tenantID, campaignID uuid.UUID, language string, butler storage.Agent, startedAt time.Time, lines []storage.TranscriptLine) uuid.UUID {
	if _, ok := s.campaigns[campaignID]; !ok {
		s.campaigns[campaignID] = storage.Campaign{ID: campaignID, TenantID: tenantID, Language: language}
	}
	butler.CampaignID = campaignID
	s.butlers[campaignID] = butler
	id := uuid.New()
	s.sessions[id] = storage.VoiceSession{ID: id, CampaignID: campaignID, StartedAt: startedAt}
	s.lines[id] = lines
	return id
}

func sampleLines() []storage.TranscriptLine {
	return []storage.TranscriptLine{
		{Seq: 1, Who: "GM", Text: "You enter the tavern."},
		{Seq: 2, Who: "Bart", Tag: "npc", Text: "Well met, travelers."},
		{Seq: 3, Who: "Alice", Tag: "player", Text: "We seek the lost relic."},
	}
}

// TestProviderResolutionChain pins gate #271's resolution order via the injected
// factory: the Butler's own config wins; else the Tenant 'llm' config; else the Groq
// env default (empty id, empty key).
func TestProviderResolutionChain(t *testing.T) {
	cipher := newCipher(t)
	tenantID := uuid.New()

	sealed := func(provider, model, key string) storage.ProviderConfig {
		ct, err := cipher.Seal([]byte(key))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		return storage.ProviderConfig{
			ID:                    uuid.New(),
			TenantID:              tenantID,
			Component:             storage.ComponentLLM,
			Provider:              provider,
			Model:                 model,
			CredentialsCiphertext: ct,
			CredentialsLast4:      crypto.Last4(key),
		}
	}

	t.Run("butler config wins", func(t *testing.T) {
		st := newFakeStore()
		cfg := sealed("anthropic", "claude-x", "sk-butler-key-1111")
		st.configs[cfg.ID] = cfg
		// A tenant config also exists — must be ignored in favour of the Butler's.
		tc := sealed("gemini", "gem-y", "gm-key-2222")
		st.byComponent[tenantID] = tc
		butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "The Keeper.", LLMProviderConfigID: uuid.NullUUID{UUID: cfg.ID, Valid: true}}
		sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

		var gotID, gotKey string
		factory := func(providerID, apiKey string) (llm.Provider, error) {
			gotID, gotKey = providerID, apiKey
			return &stubProvider{text: "A recap."}, nil
		}
		eng := NewEngine(st, cipher, observe.Discard{}, nil, WithProviderFactory(factory))
		if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
			t.Fatalf("Recap: %v", err)
		}
		if gotID != "anthropic" {
			t.Errorf("providerID = %q, want anthropic", gotID)
		}
		if gotKey != "sk-butler-key-1111" {
			t.Errorf("key = %q, want decrypted butler key", gotKey)
		}
	})

	t.Run("tenant llm config fallback", func(t *testing.T) {
		st := newFakeStore()
		tc := sealed("gemini", "gem-y", "gm-key-3333")
		st.byComponent[tenantID] = tc
		butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "The Keeper."}
		sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

		var gotID, gotKey string
		factory := func(providerID, apiKey string) (llm.Provider, error) {
			gotID, gotKey = providerID, apiKey
			return &stubProvider{text: "A recap."}, nil
		}
		eng := NewEngine(st, cipher, observe.Discard{}, nil, WithProviderFactory(factory))
		if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
			t.Fatalf("Recap: %v", err)
		}
		if gotID != "gemini" || gotKey != "gm-key-3333" {
			t.Errorf("got (%q,%q), want (gemini, gm-key-3333)", gotID, gotKey)
		}
	})

	t.Run("no config -> groq env default", func(t *testing.T) {
		st := newFakeStore()
		butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "The Keeper."}
		sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

		var gotID, gotKey string
		factory := func(providerID, apiKey string) (llm.Provider, error) {
			gotID, gotKey = providerID, apiKey
			return &stubProvider{text: "A recap."}, nil
		}
		eng := NewEngine(st, cipher, observe.Discard{}, nil, WithProviderFactory(factory))
		if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
			t.Fatalf("Recap: %v", err)
		}
		if gotID != "" || gotKey != "" {
			t.Errorf("got (%q,%q), want (\"\",\"\") the groq env default", gotID, gotKey)
		}
	})
}

// TestRecapSingleSession proves a short session produces the model's text with
// Windowed=false, the reported session id, and a system prompt carrying the Butler
// Persona and the Campaign Language.
func TestRecapSingleSession(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Wise Butler Persona."}
	sid := seedSession(st, tenantID, uuid.New(), "German", butler, time.Now(), sampleLines())

	var sysSeen string
	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "The party sought a relic.", capture: func(req llm.Request) {
			sysSeen = req.Messages[0].Text
		}}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))
	res, err := eng.Recap(context.Background(), []uuid.UUID{sid})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if res.Windowed {
		t.Error("Windowed = true, want false for a short session")
	}
	if res.Text != "The party sought a relic." {
		t.Errorf("Text = %q, want the model output", res.Text)
	}
	if len(res.SessionIDs) != 1 || res.SessionIDs[0] != sid {
		t.Errorf("SessionIDs = %v, want [%s]", res.SessionIDs, sid)
	}
	if !strings.Contains(sysSeen, "Wise Butler Persona.") {
		t.Errorf("system prompt missing Persona: %q", sysSeen)
	}
	if !strings.Contains(sysSeen, "German") {
		t.Errorf("system prompt missing Campaign Language: %q", sysSeen)
	}
}

// TestRecapMultiSession joins per-session recaps chronologically with headers, marks
// Windowed if any session windowed, and reports ids in chronological order.
func TestRecapMultiSession(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	campaignID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	t0 := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	// Seed the LATER session first to prove chronological sort.
	later := seedSession(st, tenantID, campaignID, "English", butler, t0.Add(time.Hour), sampleLines())
	earlier := seedSession(st, tenantID, campaignID, "English", butler, t0, sampleLines())

	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "session recap"}, nil
	}
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(factory))
	res, err := eng.Recap(context.Background(), []uuid.UUID{later, earlier})
	if err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if res.SessionIDs[0] != earlier || res.SessionIDs[1] != later {
		t.Errorf("SessionIDs not chronological: %v", res.SessionIDs)
	}
	if !strings.Contains(res.Text, "**Session 2026-01-02 15:04 UTC**") {
		t.Errorf("missing chronological header: %q", res.Text)
	}
	// The earlier header must appear before the later one.
	if strings.Index(res.Text, "15:04 UTC") > strings.Index(res.Text, "16:04 UTC") {
		t.Errorf("headers out of order: %q", res.Text)
	}
}

// TestMixedCampaigns rejects sessions from different campaigns.
func TestMixedCampaigns(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler}
	a := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())
	b := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "x"}, nil
	}))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{a, b}); err != ErrMixedCampaigns {
		t.Fatalf("err = %v, want ErrMixedCampaigns", err)
	}
}

// TestEmptyTranscripts: a single empty session and an all-empty multi-set both yield
// ErrNoTranscript; a mixed set skips the empty one with a note.
func TestEmptyTranscripts(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}

	t.Run("single empty -> ErrNoTranscript", func(t *testing.T) {
		st := newFakeStore()
		sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), nil)
		eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(okFactory))
		if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != ErrNoTranscript {
			t.Fatalf("err = %v, want ErrNoTranscript", err)
		}
	})

	t.Run("all empty -> ErrNoTranscript", func(t *testing.T) {
		st := newFakeStore()
		a := seedSession(st, tenantID, campaignID, "English", butler, time.Now(), nil)
		b := seedSession(st, tenantID, campaignID, "English", butler, time.Now().Add(time.Hour), nil)
		eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(okFactory))
		if _, err := eng.Recap(context.Background(), []uuid.UUID{a, b}); err != ErrNoTranscript {
			t.Fatalf("err = %v, want ErrNoTranscript", err)
		}
	})

	t.Run("one empty in multi-set is noted, not dropped", func(t *testing.T) {
		st := newFakeStore()
		full := seedSession(st, tenantID, campaignID, "English", butler, time.Now(), sampleLines())
		empty := seedSession(st, tenantID, campaignID, "English", butler, time.Now().Add(time.Hour), nil)
		eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(okFactory))
		res, err := eng.Recap(context.Background(), []uuid.UUID{full, empty})
		if err != nil {
			t.Fatalf("Recap: %v", err)
		}
		if !strings.Contains(res.Text, "no transcript lines recorded") {
			t.Errorf("empty session not noted: %q", res.Text)
		}
	})
}

func okFactory(_, _ string) (llm.Provider, error) { return &stubProvider{text: "recap text"}, nil }
