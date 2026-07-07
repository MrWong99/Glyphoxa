package observe

import (
	"testing"
	"time"
)

// baseSpy is a StageRecorder that records only the calls this test cares about:
// the usage trio (to prove they still reach base) and one non-usage method (to
// prove non-usage calls reach base and are NOT duplicated to the sink).
type baseSpy struct {
	Discard
	llm    int
	tts    int
	stt    int
	rounds int
}

func (b *baseSpy) LLMTokens(Provider, string, int, int) { b.llm++ }
func (b *baseSpy) TTSCharacters(Provider, int)          { b.tts++ }
func (b *baseSpy) STTAudioSeconds(Provider, time.Duration) {
	b.stt++
}
func (b *baseSpy) LLMRound(Provider, int, bool, time.Duration) { b.rounds++ }

// sinkSpy records the usage fan-out.
type sinkSpy struct {
	llm, tts, stt int
}

func (s *sinkSpy) LLMTokens(Provider, string, int, int)    { s.llm++ }
func (s *sinkSpy) TTSCharacters(Provider, int)             { s.tts++ }
func (s *sinkSpy) STTAudioSeconds(Provider, time.Duration) { s.stt++ }

// TestTeeUsageFansOutUsageOnly proves the tee forwards the three usage methods to
// BOTH base and sink, while a non-usage method reaches base ONLY.
func TestTeeUsageFansOutUsageOnly(t *testing.T) {
	base := &baseSpy{}
	sink := &sinkSpy{}
	rec := TeeUsage(base, sink)

	rec.LLMTokens(ProviderGroq, "m", 1, 2)
	rec.TTSCharacters(ProviderElevenLabs, 10)
	rec.STTAudioSeconds(ProviderElevenLabs, time.Second)
	rec.LLMRound(ProviderGroq, 0, false, time.Second) // non-usage: base only

	if base.llm != 1 || base.tts != 1 || base.stt != 1 {
		t.Fatalf("base usage counts = llm:%d tts:%d stt:%d, want 1/1/1", base.llm, base.tts, base.stt)
	}
	if base.rounds != 1 {
		t.Fatalf("base LLMRound = %d, want 1 (non-usage passes through)", base.rounds)
	}
	if sink.llm != 1 || sink.tts != 1 || sink.stt != 1 {
		t.Fatalf("sink usage counts = llm:%d tts:%d stt:%d, want 1/1/1", sink.llm, sink.tts, sink.stt)
	}
}
