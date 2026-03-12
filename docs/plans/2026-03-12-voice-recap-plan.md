# Voice Recap Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `/session voice-recap` command that generates dramatic voiced recaps from session transcripts using LLM + TTS, with PostgreSQL storage and GM helper NPC support.

**Architecture:** Extends the existing session/memory/command layers. New `RecapStore` interface + Postgres impl for persisting recaps. New `RecapGenerator` orchestrates LLM dramatic prompt + TTS synthesis. `GMHelper` flag on `NPCConfig` identifies the narrator voice. `ListSessions` on `SessionStore` enables campaign-scoped session lookup. Existing `/session recap` updated to accept optional `session_id`.

**Tech Stack:** Go 1.26+, PostgreSQL/pgx, disgo (Discord), existing LLM/TTS provider interfaces.

---

### Task 1: Add `GMHelper` flag to `NPCConfig` + validation

**Files:**
- Modify: `internal/config/config.go:195-231` (NPCConfig struct)
- Modify: `internal/config/loader.go:60-167` (Validate function)
- Test: `internal/config/loader_test.go`

**Step 1: Write the failing test**

Add to `internal/config/loader_test.go`:

```go
func TestValidate_DuplicateGMHelper(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			LLM: config.ProviderEntry{Name: "openai"},
			TTS: config.ProviderEntry{Name: "elevenlabs"},
		},
		NPCs: []config.NPCConfig{
			{Name: "NPC1", Engine: config.EngineCascaded, GMHelper: true},
			{Name: "NPC2", Engine: config.EngineCascaded, GMHelper: true},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate gm_helper")
	}
	if !strings.Contains(err.Error(), "gm_helper") {
		t.Errorf("expected error mentioning gm_helper, got: %s", err)
	}
}

func TestValidate_SingleGMHelper(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			LLM: config.ProviderEntry{Name: "openai"},
			TTS: config.ProviderEntry{Name: "elevenlabs"},
		},
		NPCs: []config.NPCConfig{
			{Name: "NPC1", Engine: config.EngineCascaded, GMHelper: true},
			{Name: "NPC2", Engine: config.EngineCascaded},
		},
	}

	if err := config.Validate(cfg); err != nil {
		t.Errorf("expected no error for single gm_helper, got: %s", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 -run TestValidate_DuplicateGMHelper ./internal/config/...`
Expected: FAIL — `GMHelper` field does not exist

**Step 3: Write minimal implementation**

In `internal/config/config.go`, add to `NPCConfig` struct (after `Relationships` field, line ~230):

```go
	// GMHelper designates this NPC as the GM helper. At most one NPC per
	// campaign may be flagged. The GM helper's voice is used for voiced recaps
	// and (in future) GM-assistant features. See issue #37.
	GMHelper bool `yaml:"gm_helper"`
```

In `internal/config/loader.go`, add duplicate detection inside `Validate` after the existing NPC loop (after line ~147, before `// MCP servers`):

```go
	// GM helper uniqueness check
	gmHelperCount := 0
	for i, npc := range cfg.NPCs {
		if npc.GMHelper {
			gmHelperCount++
			if gmHelperCount > 1 {
				errs = append(errs, fmt.Errorf("npcs[%d].gm_helper: only one NPC may be designated as the GM helper", i))
			}
		}
	}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 -run "TestValidate_DuplicateGMHelper|TestValidate_SingleGMHelper" ./internal/config/...`
Expected: PASS

**Step 5: Run full config test suite**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 ./internal/config/...`
Expected: PASS — no regressions

**Step 6: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/config/config.go internal/config/loader.go internal/config/loader_test.go
git commit -m "feat(config): add gm_helper flag to NPCConfig with validation

Only one NPC per campaign may be designated as the GM helper.
The GM helper's voice is used for voiced recaps (issue #33).
Full GM helper behavior tracked in issue #37.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 2: Add `SessionInfo` type and `ListSessions` to `SessionStore`

**Files:**
- Modify: `pkg/memory/types.go:1-33`
- Modify: `pkg/memory/store.go:322-348` (SessionStore interface)
- Modify: `pkg/memory/mock/mock.go:39-145` (SessionStore mock)
- Test: `pkg/memory/mock/mock.go` (compile-time assertion already present)

**Step 1: Add `SessionInfo` type to `pkg/memory/types.go`**

Append after the `IsNPC()` method (after line 33):

```go
// SessionInfo holds metadata about a recorded session.
type SessionInfo struct {
	// SessionID is the unique session identifier.
	SessionID string

	// CampaignID identifies the campaign this session belongs to.
	CampaignID string

	// StartedAt is when the session was started.
	StartedAt time.Time

	// EndedAt is when the session ended. Zero value if still active.
	EndedAt time.Time
}
```

**Step 2: Add `ListSessions` to `SessionStore` interface**

In `pkg/memory/store.go`, add to `SessionStore` interface (after `EntryCount`, before the closing `}`):

```go
	// ListSessions returns sessions for the current campaign, newest first.
	// limit caps the number of results (0 = implementation default).
	// Returns an empty (non-nil) slice when no sessions exist.
	ListSessions(ctx context.Context, limit int) ([]SessionInfo, error)
```

**Step 3: Add `ListSessions` to the mock**

In `pkg/memory/mock/mock.go`, add fields to the `SessionStore` struct:

```go
	// ListSessionsResult is returned by [SessionStore.ListSessions].
	ListSessionsResult []memory.SessionInfo

	// ListSessionsErr is returned by [SessionStore.ListSessions] when non-nil.
	ListSessionsErr error
```

Add the method implementation:

```go
// ListSessions implements [memory.SessionStore].
func (m *SessionStore) ListSessions(_ context.Context, limit int) ([]memory.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{Method: "ListSessions", Args: []any{limit}})
	if m.ListSessionsResult == nil {
		return []memory.SessionInfo{}, m.ListSessionsErr
	}
	out := make([]memory.SessionInfo, len(m.ListSessionsResult))
	copy(out, m.ListSessionsResult)
	return out, m.ListSessionsErr
}
```

**Step 4: Verify compilation**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./pkg/memory/... ./pkg/memory/mock/...`
Expected: compiles without errors

**Step 5: Fix all compilation errors from the new interface method**

The `SessionStore` interface now has a new method. All implementors must be updated:
- `pkg/memory/postgres/session_store.go` — add stub (implemented fully in Task 3)

Add a stub to `pkg/memory/postgres/session_store.go`:

```go
// ListSessions implements [memory.SessionStore].
func (s *SessionStoreImpl) ListSessions(ctx context.Context, limit int) ([]memory.SessionInfo, error) {
	return []memory.SessionInfo{}, nil // TODO: implement in Task 3
}
```

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./...`
Expected: compiles (may need to fix other implementors if any)

**Step 6: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add pkg/memory/types.go pkg/memory/store.go pkg/memory/mock/mock.go pkg/memory/postgres/session_store.go
git commit -m "feat(memory): add SessionInfo type and ListSessions to SessionStore

Adds ListSessions(ctx, limit) to the SessionStore interface for
campaign-scoped session lookup (newest first). Mock and Postgres
stub implementations included.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 3: Sessions table DDL + `ListSessions` Postgres implementation

**Files:**
- Modify: `pkg/memory/postgres/schema.go:150-179` (Migrate function)
- Modify: `pkg/memory/postgres/session_store.go` (replace stub)
- Test: `pkg/memory/postgres/session_store_test.go` (new file)

**Step 1: Add `sessions` table DDL**

In `pkg/memory/postgres/schema.go`, add a new DDL function before `Migrate`:

```go
// ddlSessions returns the DDL for the sessions metadata table.
func ddlSessions(s SchemaName) string {
	t := s.TableRef("sessions")
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    session_id   TEXT         PRIMARY KEY,
    campaign_id  TEXT         NOT NULL,
    started_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_campaign_started
    ON %s (campaign_id, started_at DESC);
`, t, t)
}
```

Add `ddlSessions(schema)` to the `statements` slice in `Migrate` (before `ddlSessionEntries`):

```go
	statements := []string{
		ddlSessions(schema),
		ddlSessionEntries(schema),
		ddlL2(schema, embeddingDimensions),
		ddlKnowledgeGraph(schema),
	}
```

**Step 2: Implement `ListSessions` in Postgres**

Replace the stub in `pkg/memory/postgres/session_store.go`:

```go
// ListSessions implements [memory.SessionStore]. It returns sessions for the
// store's campaign, ordered by started_at descending (newest first).
func (s *SessionStoreImpl) ListSessions(ctx context.Context, limit int) ([]memory.SessionInfo, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`
		SELECT session_id, campaign_id, started_at, COALESCE(ended_at, '0001-01-01T00:00:00Z')
		FROM   %s
		WHERE  campaign_id = $1
		ORDER  BY started_at DESC
		LIMIT  $2`,
		s.schema.TableRef("sessions"))

	rows, err := s.pool.Query(ctx, q, s.campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("session store: list sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.SessionInfo, error) {
		var si memory.SessionInfo
		if err := row.Scan(&si.SessionID, &si.CampaignID, &si.StartedAt, &si.EndedAt); err != nil {
			return memory.SessionInfo{}, err
		}
		return si, nil
	})
	if err != nil {
		return nil, fmt.Errorf("session store: list sessions scan: %w", err)
	}
	if sessions == nil {
		sessions = []memory.SessionInfo{}
	}
	return sessions, nil
}
```

**Step 3: Add `StartSession` and `EndSession` helper methods**

These are needed to populate the `sessions` table. Add to `pkg/memory/postgres/session_store.go`:

```go
// StartSession records a new session in the sessions metadata table.
func (s *SessionStoreImpl) StartSession(ctx context.Context, sessionID string) error {
	q := fmt.Sprintf(`
		INSERT INTO %s (session_id, campaign_id, started_at)
		VALUES ($1, $2, now())
		ON CONFLICT (session_id) DO NOTHING`,
		s.schema.TableRef("sessions"))

	_, err := s.pool.Exec(ctx, q, sessionID, s.campaignID)
	if err != nil {
		return fmt.Errorf("session store: start session: %w", err)
	}
	return nil
}

// EndSession sets the ended_at timestamp for a session.
func (s *SessionStoreImpl) EndSession(ctx context.Context, sessionID string) error {
	q := fmt.Sprintf(`
		UPDATE %s SET ended_at = now()
		WHERE  session_id = $1 AND campaign_id = $2`,
		s.schema.TableRef("sessions"))

	_, err := s.pool.Exec(ctx, q, sessionID, s.campaignID)
	if err != nil {
		return fmt.Errorf("session store: end session: %w", err)
	}
	return nil
}
```

**Step 4: Verify compilation**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./pkg/memory/...`
Expected: compiles

**Step 5: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add pkg/memory/postgres/schema.go pkg/memory/postgres/session_store.go
git commit -m "feat(postgres): add sessions table and ListSessions implementation

New sessions metadata table tracks session lifecycle (started_at,
ended_at) per campaign. ListSessions returns sessions newest-first.
StartSession/EndSession helpers populate the table.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 4: `RecapStore` interface + mock + Postgres implementation

**Files:**
- Create: `pkg/memory/recap_store.go`
- Modify: `pkg/memory/types.go` (add Recap struct)
- Modify: `pkg/memory/mock/mock.go` (add RecapStore mock)
- Modify: `pkg/memory/postgres/schema.go` (add recaps DDL)
- Create: `pkg/memory/postgres/recap_store.go`

**Step 1: Add `Recap` type to `pkg/memory/types.go`**

Append after `SessionInfo`:

```go
// Recap is the generated "Previously On..." voiced summary for a session.
type Recap struct {
	// SessionID is the session this recap was generated from.
	SessionID string

	// CampaignID identifies the campaign.
	CampaignID string

	// Text is the dramatic narrative recap text.
	Text string

	// AudioData is the rendered PCM audio bytes.
	AudioData []byte

	// SampleRate is the audio sample rate in Hz.
	SampleRate int

	// Channels is the number of audio channels.
	Channels int

	// Duration is the estimated speech duration.
	Duration time.Duration

	// GeneratedAt is when this recap was created.
	GeneratedAt time.Time
}
```

**Step 2: Create `pkg/memory/recap_store.go`**

```go
package memory

import "context"

// RecapStore persists and retrieves generated session recaps.
// Implementations must be safe for concurrent use.
type RecapStore interface {
	// SaveRecap persists a recap. If a recap for the same session already
	// exists it is replaced (upsert).
	SaveRecap(ctx context.Context, recap Recap) error

	// GetRecap retrieves the recap for the given session.
	// Returns (nil, nil) when no recap exists.
	GetRecap(ctx context.Context, sessionID string) (*Recap, error)
}
```

**Step 3: Add `RecapStore` mock to `pkg/memory/mock/mock.go`**

Append at the end of the file:

```go
// ─────────────────────────────────────────────────────────────────────────────
// RecapStore mock
// ─────────────────────────────────────────────────────────────────────────────

// RecapStore is a configurable test double for [memory.RecapStore].
type RecapStore struct {
	mu sync.Mutex

	calls []Call

	// SaveRecapErr is returned by [RecapStore.SaveRecap] when non-nil.
	SaveRecapErr error

	// GetRecapResult is returned by [RecapStore.GetRecap].
	GetRecapResult *memory.Recap

	// GetRecapErr is returned by [RecapStore.GetRecap] when non-nil.
	GetRecapErr error
}

// Calls returns a copy of all recorded method invocations.
func (m *RecapStore) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Call, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallCount returns how many times the named method was invoked.
func (m *RecapStore) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Reset clears all recorded calls without altering response configuration.
func (m *RecapStore) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

// SaveRecap implements [memory.RecapStore].
func (m *RecapStore) SaveRecap(_ context.Context, recap memory.Recap) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{Method: "SaveRecap", Args: []any{recap}})
	return m.SaveRecapErr
}

// GetRecap implements [memory.RecapStore].
func (m *RecapStore) GetRecap(_ context.Context, sessionID string) (*memory.Recap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, Call{Method: "GetRecap", Args: []any{sessionID}})
	return m.GetRecapResult, m.GetRecapErr
}

// Ensure RecapStore satisfies the interface at compile time.
var _ memory.RecapStore = (*RecapStore)(nil)
```

**Step 4: Add `recaps` DDL to `pkg/memory/postgres/schema.go`**

Add new DDL function:

```go
// ddlRecaps returns the DDL for the recaps table.
func ddlRecaps(s SchemaName) string {
	t := s.TableRef("recaps")
	sessions := s.TableRef("sessions")
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    session_id   TEXT         PRIMARY KEY REFERENCES %s(session_id),
    campaign_id  TEXT         NOT NULL,
    text         TEXT         NOT NULL,
    audio_data   BYTEA        NOT NULL,
    sample_rate  INT          NOT NULL,
    channels     INT          NOT NULL,
    duration_ns  BIGINT       NOT NULL,
    generated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
`, t, sessions)
}
```

Add `ddlRecaps(schema)` to `Migrate` statements (after `ddlSessions`):

```go
	statements := []string{
		ddlSessions(schema),
		ddlRecaps(schema),
		ddlSessionEntries(schema),
		ddlL2(schema, embeddingDimensions),
		ddlKnowledgeGraph(schema),
	}
```

**Step 5: Create `pkg/memory/postgres/recap_store.go`**

```go
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// RecapStoreImpl is the PostgreSQL-backed implementation of [memory.RecapStore].
// Obtain one via [Store.RecapStore] rather than constructing directly.
// All methods are safe for concurrent use.
type RecapStoreImpl struct {
	pool       *pgxpool.Pool
	schema     SchemaName
	campaignID string
}

// Ensure RecapStoreImpl satisfies the interface at compile time.
var _ memory.RecapStore = (*RecapStoreImpl)(nil)

// SaveRecap implements [memory.RecapStore].
func (r *RecapStoreImpl) SaveRecap(ctx context.Context, recap memory.Recap) error {
	q := fmt.Sprintf(`
		INSERT INTO %s
		    (session_id, campaign_id, text, audio_data, sample_rate, channels, duration_ns, generated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (session_id) DO UPDATE SET
		    text = EXCLUDED.text,
		    audio_data = EXCLUDED.audio_data,
		    sample_rate = EXCLUDED.sample_rate,
		    channels = EXCLUDED.channels,
		    duration_ns = EXCLUDED.duration_ns,
		    generated_at = EXCLUDED.generated_at`,
		r.schema.TableRef("recaps"))

	_, err := r.pool.Exec(ctx, q,
		recap.SessionID,
		recap.CampaignID,
		recap.Text,
		recap.AudioData,
		recap.SampleRate,
		recap.Channels,
		recap.Duration.Nanoseconds(),
		recap.GeneratedAt,
	)
	if err != nil {
		return fmt.Errorf("recap store: save recap: %w", err)
	}
	return nil
}

// GetRecap implements [memory.RecapStore].
func (r *RecapStoreImpl) GetRecap(ctx context.Context, sessionID string) (*memory.Recap, error) {
	q := fmt.Sprintf(`
		SELECT session_id, campaign_id, text, audio_data, sample_rate, channels, duration_ns, generated_at
		FROM   %s
		WHERE  session_id = $1`,
		r.schema.TableRef("recaps"))

	var (
		recap      memory.Recap
		durationNS int64
	)
	err := r.pool.QueryRow(ctx, q, sessionID).Scan(
		&recap.SessionID,
		&recap.CampaignID,
		&recap.Text,
		&recap.AudioData,
		&recap.SampleRate,
		&recap.Channels,
		&durationNS,
		&recap.GeneratedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("recap store: get recap: %w", err)
	}
	recap.Duration = time.Duration(durationNS)
	return &recap, nil
}
```

**Step 6: Expose `RecapStore` from `Store`**

In `pkg/memory/postgres/store.go`, add a field and accessor. Add `recaps *RecapStoreImpl` to the `Store` struct, initialise it in `NewStore`, and add:

```go
// RecapStore returns the RecapStore implementation.
func (s *Store) RecapStore() *RecapStoreImpl { return s.recaps }
```

**Step 7: Verify compilation**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./...`
Expected: compiles

**Step 8: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add pkg/memory/recap_store.go pkg/memory/types.go pkg/memory/mock/mock.go \
    pkg/memory/postgres/schema.go pkg/memory/postgres/recap_store.go pkg/memory/postgres/store.go
git commit -m "feat(memory): add RecapStore interface, mock, and Postgres implementation

RecapStore persists and retrieves voiced session recaps (text +
PCM audio). Postgres implementation uses upsert for idempotent
saves. Recaps table references sessions table.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 5: `RecapGenerator` — LLM + TTS orchestration

**Files:**
- Create: `internal/session/recap_generator.go`
- Create: `internal/session/recap_generator_test.go`

**Step 1: Write the failing test**

Create `internal/session/recap_generator_test.go`:

```go
package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/session"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	llmmock "github.com/MrWong99/glyphoxa/pkg/provider/llm/mock"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

func TestRecapGenerator_Generate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		entries        []memory.TranscriptEntry
		llmContent     string
		llmErr         error
		ttsAudio       [][]byte
		ttsErr         error
		storeErr       error
		wantErr        bool
		wantText       string
		wantSaveCalls  int
	}{
		{
			name: "successful generation",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Player1", Text: "I attack the dragon!", Timestamp: time.Now()},
				{SpeakerName: "Greymantle", Text: "The dragon roars!", NPCID: "npc-0", Timestamp: time.Now()},
			},
			llmContent:    "Previously, brave heroes faced the dragon...",
			ttsAudio:      [][]byte{{0x01, 0x02}, {0x03, 0x04}},
			wantText:      "Previously, brave heroes faced the dragon...",
			wantSaveCalls: 1,
		},
		{
			name:    "empty transcript",
			entries: []memory.TranscriptEntry{},
			wantErr: true,
		},
		{
			name: "llm error",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Player1", Text: "Hello", Timestamp: time.Now()},
			},
			llmErr:  context.DeadlineExceeded,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionStore := &memorymock.SessionStore{
				GetRecentResult: tt.entries,
			}
			recapStore := &memorymock.RecapStore{
				SaveRecapErr: tt.storeErr,
			}
			llmProv := &llmmock.Provider{
				CompleteContent: tt.llmContent,
				CompleteErr:     tt.llmErr,
			}
			ttsProv := &ttsmock.Provider{
				SynthesizeResult: tt.ttsAudio,
				SynthesizeErr:    tt.ttsErr,
			}

			gen := session.NewRecapGenerator(llmProv, ttsProv, recapStore)

			voice := tts.VoiceProfile{ID: "narrator", Provider: "test"}
			recap, err := gen.Generate(context.Background(), "session-1", "campaign-1", sessionStore, voice)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if recap.Text != tt.wantText {
				t.Errorf("text = %q, want %q", recap.Text, tt.wantText)
			}
			if recap.SessionID != "session-1" {
				t.Errorf("session_id = %q, want %q", recap.SessionID, "session-1")
			}
			if recapStore.CallCount("SaveRecap") != tt.wantSaveCalls {
				t.Errorf("SaveRecap calls = %d, want %d", recapStore.CallCount("SaveRecap"), tt.wantSaveCalls)
			}
		})
	}
}
```

Note: The LLM and TTS mock packages may not exist yet. Check `pkg/provider/llm/mock/` and `pkg/provider/tts/mock/` — if they exist, use their patterns. If not, create minimal mocks inline in the test file or in the mock packages following the `memorymock` pattern.

**Step 2: Run test to verify it fails**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 -run TestRecapGenerator_Generate ./internal/session/...`
Expected: FAIL — `session.NewRecapGenerator` does not exist

**Step 3: Write implementation**

Create `internal/session/recap_generator.go`:

```go
package session

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

// recapNarratorPrompt is the system prompt for generating dramatic session recaps.
const recapNarratorPrompt = `You are the dramatic narrator of an epic tabletop RPG campaign. Craft a gripping "Previously On..." recap from the session transcript below.

Guidelines:
- 200-300 words (~90-120 seconds spoken)
- Third person, past tense, vivid cinematic language
- Open with a strong hook: a memorable moment, looming threat, or unresolved question
- Highlight: key decisions, secrets revealed, dangers faced, bonds forged or broken
- Close with a cliffhanger that sets the stage for the next session
- Do NOT include dice rolls or mechanical terms — narrate outcomes only
- Do NOT invent events not in the transcript`

// RecapGenerator orchestrates LLM + TTS to produce voiced session recaps.
type RecapGenerator struct {
	llm   llm.Provider
	tts   tts.Provider
	store memory.RecapStore
}

// NewRecapGenerator creates a RecapGenerator.
func NewRecapGenerator(llmProv llm.Provider, ttsProv tts.Provider, store memory.RecapStore) *RecapGenerator {
	return &RecapGenerator{
		llm:   llmProv,
		tts:   ttsProv,
		store: store,
	}
}

// Generate reads the session transcript, calls LLM for a dramatic recap,
// synthesises TTS audio, and persists the result via RecapStore.
func (g *RecapGenerator) Generate(ctx context.Context, sessionID, campaignID string, sessionStore memory.SessionStore, voice tts.VoiceProfile) (*memory.Recap, error) {
	// Fetch transcript.
	entries, err := sessionStore.GetRecent(ctx, sessionID, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("recap: fetch transcript: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("recap: no transcript entries for session %s", sessionID)
	}

	// Format transcript for LLM.
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "[%s]: %s\n", e.SpeakerName, e.Text)
	}

	// Generate dramatic recap text.
	resp, err := g.llm.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: recapNarratorPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: sb.String()},
		},
		Temperature: 0.7,
	})
	if err != nil {
		return nil, fmt.Errorf("recap: llm complete: %w", err)
	}

	recapText := strings.TrimSpace(resp.Content)

	// Synthesise TTS audio.
	textCh := make(chan string, 1)
	go func() {
		defer close(textCh)
		textCh <- recapText
	}()

	audioCh, err := g.tts.SynthesizeStream(ctx, textCh, voice)
	if err != nil {
		return nil, fmt.Errorf("recap: tts synthesize: %w", err)
	}

	var audioBuf bytes.Buffer
	for chunk := range audioCh {
		audioBuf.Write(chunk)
	}

	now := time.Now().UTC()
	recap := &memory.Recap{
		SessionID:   sessionID,
		CampaignID:  campaignID,
		Text:        recapText,
		AudioData:   audioBuf.Bytes(),
		SampleRate:  22050, // default; could be derived from TTS provider config
		Channels:    1,
		Duration:    estimateDuration(audioBuf.Len(), 22050, 1),
		GeneratedAt: now,
	}

	if err := g.store.SaveRecap(ctx, *recap); err != nil {
		return nil, fmt.Errorf("recap: save: %w", err)
	}

	return recap, nil
}

// estimateDuration calculates audio duration from PCM byte count.
// Assumes 16-bit samples (2 bytes per sample).
func estimateDuration(byteCount, sampleRate, channels int) time.Duration {
	if sampleRate <= 0 || channels <= 0 {
		return 0
	}
	samples := byteCount / (2 * channels) // 16-bit = 2 bytes
	return time.Duration(samples) * time.Second / time.Duration(sampleRate)
}
```

**Step 4: Create LLM and TTS mock packages if missing**

Check if `pkg/provider/llm/mock/` and `pkg/provider/tts/mock/` have the right mock types. The glob results show these directories exist but have no test files. Read their contents and adapt the test imports accordingly. If the mocks don't have `CompleteContent`/`CompleteErr` fields, create them following the `memorymock` pattern.

**Step 5: Run tests**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 -run TestRecapGenerator ./internal/session/...`
Expected: PASS

**Step 6: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/session/recap_generator.go internal/session/recap_generator_test.go
git commit -m "feat(session): add RecapGenerator for voiced session recaps

RecapGenerator orchestrates LLM (dramatic narrator prompt) + TTS
to produce voiced session recaps. Stores results via RecapStore.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 6: Update `/session recap` to accept optional `session_id`

**Files:**
- Modify: `internal/discord/commands/session.go:46-65` (Definition)
- Modify: `internal/discord/commands/recap.go:63-119` (handleRecap)

**Step 1: Add `session_id` option to recap subcommand definition**

In `internal/discord/commands/session.go`, update the `recap` subcommand definition:

```go
			discord.ApplicationCommandOptionSubCommand{
				Name:        "recap",
				Description: "Show a recap of the current or most recent session",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:        "session_id",
						Description: "Session ID (defaults to current or most recent)",
						Required:    false,
					},
				},
			},
```

**Step 2: Update `handleRecap` to use `session_id` param or `ListSessions` fallback**

In `internal/discord/commands/recap.go`, update `handleRecap` to:
1. Extract optional `session_id` from the interaction data
2. If not provided and no active session, use `sessionStore.ListSessions(1)` to get the most recent
3. Fall back to current active session if available

The key change is in the session ID resolution logic (lines ~70-76 of `recap.go`):

```go
func (rc *RecapCommands) handleRecap(e *events.ApplicationCommandInteractionCreate) {
	if !rc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to view session recaps.")
		return
	}

	// Resolve session ID: explicit param > active session > most recent.
	var sessionID string
	var campaignName string

	data := e.SlashCommandInteractionData()
	if opt, ok := data.OptString("session_id"); ok && opt != "" {
		sessionID = opt
	}

	if sessionID == "" {
		info := rc.sessionMgr.Info()
		if info.SessionID != "" {
			sessionID = info.SessionID
			campaignName = info.CampaignName
		}
	}

	if sessionID == "" && rc.sessionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := rc.sessionStore.ListSessions(ctx, 1)
		if err == nil && len(sessions) > 0 {
			sessionID = sessions[0].SessionID
		}
	}

	if sessionID == "" {
		discordbot.RespondEphemeral(e, "No session data available. Start a session first with `/session start`.")
		return
	}

	discordbot.DeferReply(e)

	// ... rest of handler uses sessionID ...
```

Adjust the remaining handler body to use the resolved `sessionID` instead of `info.SessionID`. The `duration` and `status` fields should handle the case where the session may not be active.

**Step 3: Verify compilation and tests**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 ./internal/discord/commands/...`
Expected: PASS

**Step 4: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/discord/commands/session.go internal/discord/commands/recap.go
git commit -m "feat(discord): add session_id param to /session recap

The /session recap command now accepts an optional session_id
parameter. Falls back to the active session, then to the most
recent session via ListSessions.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 7: `/session voice-recap` command handler

**Files:**
- Modify: `internal/discord/commands/session.go:46-65` (add voice-recap subcommand def)
- Create: `internal/discord/commands/voice_recap.go`
- Create: `internal/discord/commands/voice_recap_test.go`

**Step 1: Add `voice-recap` subcommand to session Definition**

In `internal/discord/commands/session.go`, add to the `Options` slice in `Definition()`:

```go
			discord.ApplicationCommandOptionSubCommand{
				Name:        "voice-recap",
				Description: "Generate and play a dramatic voiced recap of a session",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:        "session_id",
						Description: "Session ID (defaults to most recent for this campaign)",
						Required:    false,
					},
				},
			},
```

Also update the fallback response message:

```go
	router.RegisterCommand("session", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: `/session start`, `/session stop`, `/session recap`, or `/session voice-recap`.")
	})
```

**Step 2: Create `internal/discord/commands/voice_recap.go`**

```go
package commands

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

// voiceRecapColor is the embed sidebar color for voice recap embeds.
const voiceRecapColor = 0x9B59B6

// VoiceRecapCommands handles the /session voice-recap slash command.
type VoiceRecapCommands struct {
	sessionMgr   *app.SessionManager
	perms        *discordbot.PermissionChecker
	generator    *session.RecapGenerator
	recapStore   memory.RecapStore
	sessionStore memory.SessionStore
	npcs         []config.NPCConfig
}

// VoiceRecapConfig holds dependencies for creating VoiceRecapCommands.
type VoiceRecapConfig struct {
	Bot          *discordbot.Bot
	SessionMgr   *app.SessionManager
	Perms        *discordbot.PermissionChecker
	Generator    *session.RecapGenerator
	RecapStore   memory.RecapStore
	SessionStore memory.SessionStore
	NPCs         []config.NPCConfig
}

// NewVoiceRecapCommands creates a VoiceRecapCommands and registers the handler.
func NewVoiceRecapCommands(cfg VoiceRecapConfig) *VoiceRecapCommands {
	vrc := &VoiceRecapCommands{
		sessionMgr:   cfg.SessionMgr,
		perms:        cfg.Perms,
		generator:    cfg.Generator,
		recapStore:   cfg.RecapStore,
		sessionStore: cfg.SessionStore,
		npcs:         cfg.NPCs,
	}
	cfg.Bot.Router().RegisterHandler("session/voice-recap", vrc.handleVoiceRecap)
	return vrc
}

// gmHelperVoice returns the VoiceProfile of the GM helper NPC.
// Falls back to the first NPC if no GM helper is designated.
func (vrc *VoiceRecapCommands) gmHelperVoice() tts.VoiceProfile {
	for _, npc := range vrc.npcs {
		if npc.GMHelper {
			return tts.VoiceProfile{
				ID:          npc.Voice.VoiceID,
				Provider:    npc.Voice.Provider,
				PitchShift:  npc.Voice.PitchShift,
				SpeedFactor: npc.Voice.SpeedFactor,
			}
		}
	}
	// Fallback to first NPC.
	if len(vrc.npcs) > 0 {
		npc := vrc.npcs[0]
		return tts.VoiceProfile{
			ID:          npc.Voice.VoiceID,
			Provider:    npc.Voice.Provider,
			PitchShift:  npc.Voice.PitchShift,
			SpeedFactor: npc.Voice.SpeedFactor,
		}
	}
	return tts.VoiceProfile{}
}

// handleVoiceRecap handles /session voice-recap.
func (vrc *VoiceRecapCommands) handleVoiceRecap(e *events.ApplicationCommandInteractionCreate) {
	if !vrc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to generate voice recaps.")
		return
	}

	if !vrc.sessionMgr.IsActive() {
		discordbot.RespondEphemeral(e, "A voice session must be active to play the recap. Start one with `/session start`.")
		return
	}

	// Resolve session ID.
	var sessionID string
	data := e.SlashCommandInteractionData()
	if opt, ok := data.OptString("session_id"); ok && opt != "" {
		sessionID = opt
	}

	if sessionID == "" && vrc.sessionStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := vrc.sessionStore.ListSessions(ctx, 1)
		if err == nil && len(sessions) > 0 {
			sessionID = sessions[0].SessionID
		}
	}

	if sessionID == "" {
		discordbot.RespondEphemeral(e, "No previous session found for this campaign.")
		return
	}

	discordbot.DeferReply(e)

	// Check cache.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var recap *memory.Recap
	if vrc.recapStore != nil {
		cached, err := vrc.recapStore.GetRecap(ctx, sessionID)
		if err != nil {
			slog.Warn("voice-recap: cache lookup failed", "session_id", sessionID, "err", err)
		}
		if cached != nil {
			recap = cached
		}
	}

	// Generate if not cached.
	if recap == nil {
		if vrc.generator == nil {
			discordbot.FollowUp(e, "Voice recaps require LLM and TTS providers to be configured.")
			return
		}

		voice := vrc.gmHelperVoice()
		info := vrc.sessionMgr.Info()

		generated, err := vrc.generator.Generate(ctx, sessionID, info.CampaignName, vrc.sessionStore, voice)
		if err != nil {
			slog.Error("voice-recap: generation failed", "session_id", sessionID, "err", err)
			discordbot.FollowUp(e, fmt.Sprintf("Failed to generate voice recap: %v", err))
			return
		}
		recap = generated
	}

	// Play audio via mixer.
	orch := vrc.sessionMgr.Orchestrator()
	if orch == nil {
		discordbot.FollowUp(e, "Session is not fully active. Cannot play audio.")
		return
	}

	audioCh := make(chan []byte, 1)
	go func() {
		defer close(audioCh)
		audioCh <- recap.AudioData
	}()

	segment := &audio.AudioSegment{
		NPCID:      "narrator",
		Audio:      audioCh,
		SampleRate: recap.SampleRate,
		Channels:   recap.Channels,
		Priority:   10, // high priority — recap should not be interrupted
	}

	mixer := vrc.sessionMgr.Mixer()
	if mixer != nil {
		mixer.Enqueue(segment, segment.Priority)
	}

	// Post text embed alongside.
	now := time.Now().UTC()
	embed := discord.Embed{
		Title:       "Previously, on your campaign...",
		Description: recap.Text,
		Color:       voiceRecapColor,
		Footer:      &discord.EmbedFooter{Text: fmt.Sprintf("Session: %s | Duration: %s", sessionID, recap.Duration.Truncate(time.Second))},
		Timestamp:   &now,
	}
	discordbot.FollowUpEmbed(e, embed)
}
```

**Note:** This references `vrc.sessionMgr.Mixer()` — a `Mixer()` accessor must be added to `SessionManager` if it doesn't exist. Check and add:

```go
// Mixer returns the active session's mixer. Returns nil if no session is active.
func (sm *SessionManager) Mixer() audio.Mixer {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.mixer
}
```

**Step 3: Verify compilation**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./...`
Expected: compiles (fix any import issues)

**Step 4: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/discord/commands/voice_recap.go internal/discord/commands/session.go \
    internal/app/session_manager.go
git commit -m "feat(discord): add /session voice-recap command

Generates dramatic voiced recaps via LLM + TTS, plays audio into
the active voice session via the mixer, and posts text as an embed.
Uses GM helper NPC voice (falls back to first NPC). Caches results
in RecapStore.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 8: App wiring + main.go integration

**Files:**
- Modify: `internal/app/app.go` (expose RecapStore)
- Modify: `cmd/glyphoxa/main.go:170-210` (wire VoiceRecapCommands)

**Step 1: Expose RecapStore from App**

In `internal/app/app.go`, add a `recapStore` field to `App` and initialise it in `initMemory`:

```go
// In App struct:
	recapStore memory.RecapStore

// In initMemory, after store is created:
	if a.recapStore == nil {
		a.recapStore = store.RecapStore()
	}

// Accessor:
// RecapStore returns the recap store. May be nil if memory is not configured.
func (a *App) RecapStore() memory.RecapStore { return a.recapStore }
```

**Step 2: Wire VoiceRecapCommands in main.go**

In `cmd/glyphoxa/main.go`, after the existing `NewRecapCommands` call (~line 196):

```go
		// Voice recap — requires LLM + TTS.
		if providers.LLM != nil && providers.TTS != nil && application.RecapStore() != nil {
			recapGen := session.NewRecapGenerator(providers.LLM, providers.TTS, application.RecapStore())
			commands.NewVoiceRecapCommands(commands.VoiceRecapConfig{
				Bot:          bot,
				SessionMgr:   sessionMgr,
				Perms:        perms,
				Generator:    recapGen,
				RecapStore:   application.RecapStore(),
				SessionStore: application.SessionStore(),
				NPCs:         cfg.NPCs,
			})
		}
```

Also pass the `Summariser` to `RecapConfig` if the LLM provider is available (existing code already does this — verify):

```go
		// Update existing RecapCommands to pass LLM summariser.
		var summariser session.Summariser
		if providers.LLM != nil {
			summariser = session.NewLLMSummariser(providers.LLM)
		}
		commands.NewRecapCommands(commands.RecapConfig{
			Bot:          bot,
			SessionMgr:   sessionMgr,
			Perms:        perms,
			SessionStore: application.SessionStore(),
			Summariser:   summariser,
		})
```

**Step 3: Verify compilation**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go build ./...`
Expected: compiles

**Step 4: Run full test suite**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 ./... 2>&1 | grep -E "^(ok|FAIL|---)" | head -30`
Expected: all tests pass (except pre-existing whisper build failure)

**Step 5: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/app/app.go cmd/glyphoxa/main.go
git commit -m "feat: wire VoiceRecapCommands in app and main

Exposes RecapStore from App. Creates RecapGenerator and wires
VoiceRecapCommands when LLM, TTS, and RecapStore are available.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 9: Wire session lifecycle to sessions table

**Files:**
- Modify: `internal/app/session_manager.go:119-300` (Start/Stop methods)

**Step 1: Record session start in sessions table**

In `SessionManager.Start()`, after generating the session ID (~line 136) and before connecting to voice, call `StartSession` on the Postgres store. This requires a type assertion since `SessionStore` doesn't expose `StartSession` directly:

```go
	// Record session in sessions metadata table if Postgres-backed.
	if starter, ok := sm.sessionStore.(interface {
		StartSession(ctx context.Context, sessionID string) error
	}); ok {
		if err := starter.StartSession(ctx, sessionID); err != nil {
			slog.Warn("session: failed to record session start", "session_id", sessionID, "err", err)
		}
	}
```

**Step 2: Record session end in sessions table**

In `SessionManager.Stop()`, after final consolidation (~line 322) and before cancelling context:

```go
	// Record session end in sessions metadata table.
	if ender, ok := sm.sessionStore.(interface {
		EndSession(ctx context.Context, sessionID string) error
	}); ok {
		if err := ender.EndSession(ctx, sessionID); err != nil {
			slog.Warn("session: failed to record session end", "session_id", sessionID, "err", err)
		}
	}
```

**Step 3: Verify compilation and tests**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 ./internal/app/...`
Expected: PASS

**Step 4: Commit**

```bash
cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap
git add internal/app/session_manager.go
git commit -m "feat(session): record session start/end in sessions table

SessionManager.Start and Stop now write to the sessions metadata
table when backed by PostgreSQL, enabling ListSessions for recap
session lookup.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 10: Final verification + cleanup

**Step 1: Run full test suite**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && go test -race -count=1 ./... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
Expected: all pass (except whisper)

**Step 2: Run linter**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && make lint`
Expected: no new lint errors

**Step 3: Run vet**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && make vet`
Expected: no issues

**Step 4: Verify all new files have compile-time interface assertions**

Check that each implementation file has `var _ Interface = (*Impl)(nil)` at the top:
- `pkg/memory/postgres/recap_store.go` — `var _ memory.RecapStore = (*RecapStoreImpl)(nil)`
- `pkg/memory/mock/mock.go` — `var _ memory.RecapStore = (*RecapStore)(nil)`

**Step 5: Review git log**

Run: `cd /home/luk/Desktop/git/Glyphoxa/.worktrees/feat-voice-recap && git log --oneline feat/voice-recap --not main`
Expected: clean commit history for the feature branch
