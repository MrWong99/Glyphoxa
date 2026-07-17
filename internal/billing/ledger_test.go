package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fixedNow pins the ledger's clock so day bucketing is deterministic.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestLedgerAccumulatesAndBuckets(t *testing.T) {
	tenant := uuid.New()
	at := time.Date(2026, 7, 17, 21, 30, 0, 0, time.UTC)
	l := NewLedger(tenant, fixedNow(at))

	// Two LLM calls on the same (provider, model) fold into ONE bucket; a second
	// model and the TTS/STT components get buckets of their own.
	l.LLMTokens(observe.ProviderGroq, "openai/gpt-oss-120b", 1000, 500)
	l.LLMTokens(observe.ProviderGroq, "openai/gpt-oss-120b", 200, 100)
	l.LLMTokens(observe.ProviderGroq, "qwen/qwen3-32b", 10, 10)
	l.TTSCharacters(observe.ProviderElevenLabs, 400)
	l.STTAudioSeconds(observe.ProviderElevenLabs, 90*time.Second)

	var got []storage.UsageRow
	err := l.Flush(context.Background(), func(_ context.Context, rows []storage.UsageRow) error {
		got = rows
		return nil
	})
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4 buckets", len(got))
	}

	byKey := map[string]storage.UsageRow{}
	for _, r := range got {
		if r.TenantID != tenant {
			t.Errorf("row tenant = %s, want %s", r.TenantID, tenant)
		}
		wantDay := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
		if !r.Day.Equal(wantDay) {
			t.Errorf("row day = %v, want %v", r.Day, wantDay)
		}
		byKey[string(r.Component)+"/"+r.Provider+"/"+r.Model] = r
	}

	llm := byKey["llm/groq/openai/gpt-oss-120b"]
	if llm.LLMInputTokens != 1200 || llm.LLMOutputTokens != 600 {
		t.Errorf("llm tokens = %d/%d, want 1200/600", llm.LLMInputTokens, llm.LLMOutputTokens)
	}
	wantUSD := spend.EstimateLLMUSD(observe.ProviderGroq, "openai/gpt-oss-120b", 1000, 500) +
		spend.EstimateLLMUSD(observe.ProviderGroq, "openai/gpt-oss-120b", 200, 100)
	if diff := llm.EstimatedUSD - wantUSD; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("llm estimated USD = %v, want %v", llm.EstimatedUSD, wantUSD)
	}

	tts := byKey["tts/elevenlabs/"]
	if tts.TTSCharacters != 400 {
		t.Errorf("tts chars = %d, want 400", tts.TTSCharacters)
	}
	stt := byKey["stt/elevenlabs/"]
	if stt.STTAudioSeconds != 90 {
		t.Errorf("stt seconds = %v, want 90", stt.STTAudioSeconds)
	}
	if stt.EstimatedUSD <= 0 || tts.EstimatedUSD <= 0 {
		t.Errorf("estimates must be positive: tts=%v stt=%v", tts.EstimatedUSD, stt.EstimatedUSD)
	}
}

func TestLedgerFlushDrainsBuffer(t *testing.T) {
	l := NewLedger(uuid.New(), fixedNow(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)))
	l.TTSCharacters(observe.ProviderElevenLabs, 100)

	calls := 0
	flush := func(_ context.Context, rows []storage.UsageRow) error {
		calls++
		return nil
	}
	if err := l.Flush(context.Background(), flush); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	// Empty buffer: no second call.
	if err := l.Flush(context.Background(), flush); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if calls != 1 {
		t.Fatalf("flush calls = %d, want 1 (empty buffer must not flush)", calls)
	}
}

func TestLedgerFlushErrorRetainsRows(t *testing.T) {
	l := NewLedger(uuid.New(), fixedNow(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)))
	l.TTSCharacters(observe.ProviderElevenLabs, 100)

	boom := errors.New("db down")
	if err := l.Flush(context.Background(), func(context.Context, []storage.UsageRow) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Flush err = %v, want %v", err, boom)
	}

	// More usage lands after the failed flush; the retry must carry BOTH the
	// merged-back snapshot and the new usage.
	l.TTSCharacters(observe.ProviderElevenLabs, 50)

	var got []storage.UsageRow
	if err := l.Flush(context.Background(), func(_ context.Context, rows []storage.UsageRow) error {
		got = rows
		return nil
	}); err != nil {
		t.Fatalf("retry Flush: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	if got[0].TTSCharacters != 150 {
		t.Fatalf("tts chars after retry = %d, want 150 (100 merged back + 50 new)", got[0].TTSCharacters)
	}
}
