package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// usageRecorder is a fake [UsageWriter]: it keeps every batch, and fails the
// first len(errs) writes with the queued errors.
type usageRecorder struct {
	mu   sync.Mutex
	errs []error
	rows []storage.UsageRow
	n    int
}

func (u *usageRecorder) AddUsage(_ context.Context, rows []storage.UsageRow) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.n++
	if len(u.errs) > 0 {
		err := u.errs[0]
		u.errs = u.errs[1:]
		return err
	}
	u.rows = append(u.rows, rows...)
	return nil
}

// fixedAllowance is a fake [AllowanceChecker].
type fixedAllowance struct {
	state spend.AllowanceState
	err   error
	calls int
}

func (f *fixedAllowance) AllowanceState(context.Context, uuid.UUID) (spend.AllowanceState, error) {
	f.calls++
	return f.state, f.err
}

func exhausted() *fixedAllowance {
	five := 5.0
	return &fixedAllowance{state: spend.AllowanceState{IncludedUSD: &five, MonthUSD: 5}}
}

// countingBase proves usage still reaches the production recorder.
type countingBase struct {
	observe.Discard
	llm int
}

func (b *countingBase) LLMTokens(observe.Provider, string, int, int) { b.llm++ }

// A call's usage reaches the ledger for the right tenant under the right
// (component, provider, model) price row, the production recorder, and the
// estimate the call site logs.
func TestOffSession_UsageReachesLedgerBaseAndEstimate(t *testing.T) {
	usage := &usageRecorder{}
	base := &countingBase{}
	tenant := uuid.New()

	call, err := NewOffSession(usage, nil, nil).Begin(context.Background(), tenant, base)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	call.Recorder().LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1000, 500)
	call.Recorder().TTSCharacters(observe.ProviderElevenLabs, "eleven_v4_turbo", 120)
	if err := call.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if base.llm != 1 {
		t.Errorf("production recorder saw %d LLM calls, want 1", base.llm)
	}
	if len(usage.rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2 (llm + tts)", len(usage.rows))
	}
	var sum float64
	for _, r := range usage.rows {
		if r.TenantID != tenant {
			t.Errorf("row tenant = %s, want %s", r.TenantID, tenant)
		}
		if r.Component == storage.ComponentLLM {
			if r.Provider != "groq" || r.Model != "llama-3.3-70b-versatile" || r.LLMInputTokens != 1000 || r.LLMOutputTokens != 500 {
				t.Errorf("llm row = %+v", r)
			}
		}
		sum += r.EstimatedUSD
	}
	if got := call.EstimatedUSD(); got <= 0 || fmt.Sprintf("%.9f", got) != fmt.Sprintf("%.9f", sum) {
		t.Errorf("EstimatedUSD = %v, want the ledger's sum %v", got, sum)
	}
}

// An exhausted allowance refuses BEFORE anything is opened; a tenant whose
// plan carries no allowance (BYOK) and a deployment with no gate (allowlist)
// both pass.
func TestOffSession_AllowanceGate(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()

	if _, err := NewOffSession(&usageRecorder{}, exhausted(), nil).Begin(ctx, tenant, nil); !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("exhausted: Begin = %v, want ErrAllowanceExhausted", err)
	}
	if err := NewOffSession(&usageRecorder{}, exhausted(), nil).Check(ctx, tenant); !errors.Is(err, ErrAllowanceExhausted) {
		t.Fatalf("exhausted: Check = %v, want ErrAllowanceExhausted", err)
	}

	byok := &fixedAllowance{} // IncludedUSD nil: the plan has no allowance
	if _, err := NewOffSession(&usageRecorder{}, byok, nil).Begin(ctx, tenant, nil); err != nil {
		t.Fatalf("BYOK plan: Begin = %v, want nil", err)
	}
	if byok.calls != 1 {
		t.Errorf("allowance read %d times, want 1", byok.calls)
	}
	if _, err := NewOffSession(&usageRecorder{}, nil, nil).Begin(ctx, tenant, nil); err != nil {
		t.Fatalf("no gate: Begin = %v, want nil", err)
	}
}

// A gate that cannot be evaluated fails closed.
func TestOffSession_AllowanceReadErrorFailsClosed(t *testing.T) {
	boom := errors.New("db down")
	_, err := NewOffSession(&usageRecorder{}, &fixedAllowance{err: boom}, nil).Begin(context.Background(), uuid.New(), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("Begin = %v, want the read error", err)
	}
	if errors.Is(err, ErrAllowanceExhausted) {
		t.Fatal("a read error must not be reported as an exhausted allowance")
	}
}

// Close retries a write that provably did not commit, and lands the rows once.
func TestOffSession_CloseRetriesUncommittedWrite(t *testing.T) {
	usage := &usageRecorder{errs: []error{errors.New("conn refused")}}
	call, _ := NewOffSession(usage, nil, nil).Begin(context.Background(), uuid.New(), nil)
	call.Recorder().LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 10, 10)
	if err := call.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if usage.n != 2 || len(usage.rows) != 1 || usage.rows[0].LLMInputTokens != 10 {
		t.Fatalf("writes = %d, rows = %+v; want a retry landing the row exactly once", usage.n, usage.rows)
	}
}

// A write whose commit outcome is unknown is NOT sent again (it may be in the
// table already), and a second Close has nothing left to send.
func TestOffSession_CloseNeverResendsUnknownOutcome(t *testing.T) {
	usage := &usageRecorder{errs: []error{fmt.Errorf("timeout: %w", storage.ErrCommitOutcomeUnknown)}}
	call, _ := NewOffSession(usage, nil, nil).Begin(context.Background(), uuid.New(), nil)
	call.Recorder().LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 10, 10)
	if err := call.Close(context.Background()); !errors.Is(err, storage.ErrCommitOutcomeUnknown) {
		t.Fatalf("Close = %v, want ErrCommitOutcomeUnknown", err)
	}
	_ = call.Close(context.Background())
	if usage.n != 1 || len(usage.rows) != 0 {
		t.Fatalf("writes = %d, rows = %d; want exactly one attempt and nothing re-sent", usage.n, len(usage.rows))
	}
}

type ctxUsage struct{ rows int }

func (u *ctxUsage) AddUsage(ctx context.Context, rows []storage.UsageRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u.rows += len(rows)
	return nil
}

// Close lands usage even when the request's context is already cancelled.
func TestOffSession_CloseSurvivesCancelledContext(t *testing.T) {
	usage := &ctxUsage{}
	call, _ := NewOffSession(usage, nil, nil).Begin(context.Background(), uuid.New(), nil)
	call.Recorder().LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := call.Close(ctx); err != nil {
		t.Fatalf("Close on a cancelled ctx: %v", err)
	}
	if usage.rows != 1 {
		t.Fatalf("rows = %d, want 1", usage.rows)
	}
}

// A sound generation lands under the tts component with its model id and is
// part of the call's estimate.
func TestOffSession_Sound(t *testing.T) {
	usage := &usageRecorder{}
	call, _ := NewOffSession(usage, nil, nil).Begin(context.Background(), uuid.New(), nil)
	call.Sound(observe.ProviderElevenLabs, "eleven_text_to_sound_v2", 5*time.Second, 200)
	_ = call.Close(context.Background())
	if len(usage.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(usage.rows))
	}
	r := usage.rows[0]
	if r.Component != storage.ComponentTTS || r.Model != "eleven_text_to_sound_v2" || r.TTSCharacters != 200 {
		t.Errorf("sound row = %+v", r)
	}
	if call.EstimatedUSD() != r.EstimatedUSD {
		t.Errorf("EstimatedUSD = %v, want %v", call.EstimatedUSD(), r.EstimatedUSD)
	}
}
