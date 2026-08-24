package observe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scrape drives the adapter's /metrics handler through an httptest server and
// returns the exposition text — the same bytes a Prometheus would pull.
func scrape(t *testing.T, rec *PrometheusRecorder) string {
	t.Helper()
	srv := httptest.NewServer(rec.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status %d", resp.StatusCode)
	}
	return string(body)
}

func TestPrometheusScrapeExposesSeries(t *testing.T) {
	rec := NewPrometheusRecorder()

	// Exercise both contracts so each family appears with a non-zero sample.
	rec.InboundFramesDropped("guild-123", 3)
	rec.InboundUndecodableFrame("guild-123")
	rec.SessionOpened("guild-123")
	rec.PlaybackFinished("guild-123", true)
	rec.BargeCancelled("guild-123")
	rec.DAVEDecryptHook()()

	rec.ResponseLatency(RoleCharacter, 900*time.Millisecond)
	rec.VADHangover(480 * time.Millisecond)
	rec.STTRequest(ProviderElevenLabs, 300*time.Millisecond)
	rec.LLMRound(ProviderGemini, 0, true, 1200*time.Millisecond)
	rec.LLMTurn(ProviderGemini, 1500*time.Millisecond)
	rec.TTSTimeToFirstByte(ProviderElevenLabs, 250*time.Millisecond)
	rec.ProviderCall(StageLLM, ProviderGemini, OutcomeOK)
	rec.ProviderError(StageTTS, ProviderElevenLabs)
	rec.TurnOutcome(TurnFirstAudio, ReasonNone)
	rec.TurnOutcome(TurnAbandoned, ReasonNoFirstAudio)
	rec.TurnOutcome(TurnYielded, ReasonSupersessionGrace)
	rec.TurnOutcome(TurnAbandoned, ReasonBarge)
	rec.TurnOutcome(TurnAbandoned, ReasonTTSError)
	rec.TurnOutcome(TurnAbandoned, ReasonProviderError)

	rec.MemoryRecall(RecallHit, 4*time.Millisecond)
	rec.MemoryRecall(RecallMiss, 120*time.Millisecond)
	rec.MemoryRecall(RecallSkip, 250*time.Millisecond)

	rec.KGFacts(FactsOK, 800*time.Microsecond)
	rec.KGFacts(FactsEmpty, 600*time.Microsecond)
	rec.KGFacts(FactsDegraded, 60*time.Millisecond)

	rec.MalformedToolGen(ProviderGroq, MalformedStreamError)

	out := scrape(t, rec)

	// Every family is present and namespaced glyphoxa_voice_* (embedding_backlog
	// is process-level glyphoxa_ per ADR-0032), with the agreed labels.
	wantSubstrings := []string{
		`glyphoxa_voice_inbound_frames_dropped_total 3`,
		`glyphoxa_voice_inbound_undecodable_frames_total 1`,
		`glyphoxa_voice_dave_decrypt_errors_total 1`,
		`glyphoxa_voice_sessions 1`,
		`glyphoxa_voice_playback_total{interrupted="true"} 1`,
		`glyphoxa_voice_barge_cancels_total 1`,
		`glyphoxa_voice_response_latency_seconds_bucket{agent_role="character"`,
		`glyphoxa_voice_vad_hangover_seconds_bucket`,
		`glyphoxa_voice_stt_request_seconds_bucket{provider="elevenlabs"`,
		`glyphoxa_voice_llm_round_seconds_bucket{had_tool_call="true",provider="gemini",round_index="0"`,
		`glyphoxa_voice_llm_turn_seconds_bucket{provider="gemini"`,
		`glyphoxa_voice_tts_ttfb_seconds_bucket{provider="elevenlabs"`,
		`glyphoxa_voice_provider_calls_total{outcome="ok",provider="gemini",stage="llm"} 1`,
		`glyphoxa_voice_provider_errors_total{provider="elevenlabs",stage="tts"} 1`,
		`glyphoxa_voice_turn_total{outcome="first_audio",reason="none"} 1`,
		`glyphoxa_voice_turn_total{outcome="abandoned",reason="no_first_audio"} 1`,
		`glyphoxa_voice_turn_total{outcome="yielded",reason="supersession_grace"} 1`,
		`glyphoxa_voice_turn_total{outcome="abandoned",reason="barge"} 1`,
		`glyphoxa_voice_turn_total{outcome="abandoned",reason="tts_error"} 1`,
		`glyphoxa_voice_turn_total{outcome="abandoned",reason="provider_error"} 1`,
		`glyphoxa_voice_memory_recall_total{outcome="hit"} 1`,
		`glyphoxa_voice_memory_recall_total{outcome="miss"} 1`,
		`glyphoxa_voice_memory_recall_total{outcome="skip"} 1`,
		`glyphoxa_embedding_backlog 0`,
		`glyphoxa_kg_facts_total{outcome="ok"} 1`,
		`glyphoxa_kg_facts_total{outcome="empty"} 1`,
		`glyphoxa_kg_facts_total{outcome="degraded"} 1`,
		`glyphoxa_llm_malformed_toolgen_total{path="stream_error",provider="groq"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

// TestHighlightClassifyMetric pins the Session-Highlights classify-outcome series
// (#428, ADR-0032): one increment per outcome exposes the exact
// glyphoxa_voice_highlight_classify_total name with the bounded outcome label, and
// the label space is exactly the three fixed values.
func TestHighlightClassifyMetric(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.HighlightClassify(HighlightOK)
	rec.HighlightClassify(HighlightParseFailed)
	rec.HighlightClassify(HighlightLLMError)

	out := scrape(t, rec)

	wantSubstrings := []string{
		`glyphoxa_voice_highlight_classify_total{outcome="ok"} 1`,
		`glyphoxa_voice_highlight_classify_total{outcome="parse_failed"} 1`,
		`glyphoxa_voice_highlight_classify_total{outcome="llm_error"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

// TestJobRunnerMetrics pins the background-job-runner series (#286, ADR-0049):
// the three families expose their exact glyphoxa_jobs_* / glyphoxa_job_* names
// and kind/outcome labels, and SetJobBacklog is a Set (idempotent) not an Inc —
// two Sets to 4 then 2 leave the gauge at 2.
func TestJobRunnerMetrics(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.JobOutcome("highlight.enrich", "done")
	rec.JobOutcome("highlight.enrich", "retry")
	rec.JobOutcome("highlight.enrich", "dead")
	rec.JobDuration("highlight.enrich", 2*time.Second)

	// Set-not-Inc: a re-Set from a fresh COUNT overwrites, never accumulates.
	rec.SetJobBacklog("highlight.enrich", 4)
	rec.SetJobBacklog("highlight.enrich", 2)

	out := scrape(t, rec)

	wantSubstrings := []string{
		`glyphoxa_jobs_total{kind="highlight.enrich",outcome="done"} 1`,
		`glyphoxa_jobs_total{kind="highlight.enrich",outcome="retry"} 1`,
		`glyphoxa_jobs_total{kind="highlight.enrich",outcome="dead"} 1`,
		`glyphoxa_job_duration_seconds_bucket{kind="highlight.enrich",le="5"} 1`,
		`glyphoxa_job_duration_seconds_count{kind="highlight.enrich"} 1`,
		`glyphoxa_jobs_backlog{kind="highlight.enrich"} 2`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

// TestTranscriptSSEMetrics pins the live-transcript SSE series (#612,
// ADR-0032): both families are process-level with NO labels (a session id is
// never a label), the lag counter accumulates, and the subscriber gauge is a
// Set (idempotent) not an Inc — Sets to 5 then 3 leave it at 3.
func TestTranscriptSSEMetrics(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.TranscriptSSELagged()

	// Set-not-Inc: a re-Set from a fresh count overwrites, never accumulates.
	rec.SetTranscriptSSESubscribers(5)
	rec.SetTranscriptSSESubscribers(3)

	out := scrape(t, rec)

	wantSubstrings := []string{
		"glyphoxa_transcript_sse_lagged_total 1",
		"glyphoxa_transcript_sse_subscribers 3",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

// TestWiredHistogramsAndProviderCounters is the #125 AC pin: after one real
// observation on each previously-reserved instrument, every one of the six
// histogram families exposes a non-empty series, the STT and TTS provider-call /
// provider-error counters carry the right stage labels, and NO help text still
// advertises "RESERVED" (the markers are dropped as each emit-site is wired).
func TestWiredHistogramsAndProviderCounters(t *testing.T) {
	rec := NewPrometheusRecorder()

	// One observation on each of the six formerly-reserved histograms.
	rec.VADHangover(384 * time.Millisecond)
	rec.CodecDecode(2 * time.Millisecond)
	rec.CodecEncode(1 * time.Millisecond)
	rec.STTRequest(ProviderElevenLabs, 700*time.Millisecond)
	rec.TTSTotal(ProviderElevenLabs, 900*time.Millisecond)
	rec.LLMTurn(ProviderGroq, 1500*time.Millisecond)

	// STT and TTS provider health: a call + error at each stage.
	rec.ProviderCall(StageSTT, ProviderElevenLabs, OutcomeOK)
	rec.ProviderError(StageSTT, ProviderElevenLabs)
	rec.ProviderCall(StageTTS, ProviderElevenLabs, OutcomeError)
	rec.ProviderError(StageTTS, ProviderElevenLabs)

	out := scrape(t, rec)

	wantSeries := []string{
		`glyphoxa_voice_vad_hangover_seconds_count 1`,
		`glyphoxa_voice_codec_decode_seconds_count 1`,
		`glyphoxa_voice_codec_encode_seconds_count 1`,
		`glyphoxa_voice_stt_request_seconds_count{provider="elevenlabs"} 1`,
		`glyphoxa_voice_tts_total_seconds_count{provider="elevenlabs"} 1`,
		`glyphoxa_voice_llm_turn_seconds_count{provider="groq"} 1`,
		`glyphoxa_voice_provider_calls_total{outcome="ok",provider="elevenlabs",stage="stt"} 1`,
		`glyphoxa_voice_provider_calls_total{outcome="error",provider="elevenlabs",stage="tts"} 1`,
		`glyphoxa_voice_provider_errors_total{provider="elevenlabs",stage="stt"} 1`,
		`glyphoxa_voice_provider_errors_total{provider="elevenlabs",stage="tts"} 1`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q\n%s", want, filterGlyphoxa(out))
		}
	}

	// AC: the RESERVED markers are removed from the help text as each is wired.
	if strings.Contains(out, "RESERVED") {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "RESERVED") {
				t.Errorf("help text still carries RESERVED after wiring: %q", line)
			}
		}
	}
}

// TestTTSTotalDeliverSpanBucketsAndHelp pins the #239 re-scope: tts_total is a
// deliver span carrying its own wide buckets (0.5–60s), NOT the shared sub-5s SLO
// buckets, and its help text names it a deliver span and points provider latency at
// tts_ttfb.
func TestTTSTotalDeliverSpanBucketsAndHelp(t *testing.T) {
	rec := NewPrometheusRecorder()
	rec.TTSTotal(ProviderElevenLabs, 12*time.Second)
	out := scrape(t, rec)

	wantHelp := `# HELP glyphoxa_voice_tts_total_seconds TTS deliver span: synthesis plus paced playback delivery of one sentence. Provider latency lives in tts_ttfb.`
	if !strings.Contains(out, wantHelp) {
		t.Errorf("tts_total help missing/wrong; want:\n%s\ngot:\n%s", wantHelp, filterGlyphoxa(out))
	}
	// A wide bin must exist (deliver buckets), and the sub-second SLO bin must NOT.
	// (Prometheus orders the histogram's le label after the user labels.)
	if !strings.Contains(out, `glyphoxa_voice_tts_total_seconds_bucket{provider="elevenlabs",le="60"}`) {
		t.Errorf("tts_total missing the wide le=60 bucket:\n%s", filterGlyphoxa(out))
	}
	if strings.Contains(out, `glyphoxa_voice_tts_total_seconds_bucket{provider="elevenlabs",le="0.05"`) {
		t.Errorf("tts_total still carries the shared SLO buckets (le=0.05); want its own wide buckets:\n%s", filterGlyphoxa(out))
	}
}

// TestUsageMeters_TokensCharactersAudioSeconds is the #127 AC pin (ADR-0045): the
// three usage meters expose their series with provider-only bounds — LLM tokens
// split by a required direction label (Groq prices input/output differently), TTS
// characters and STT audio-seconds by provider. The model passed to LLMTokens is
// for the spend meter (ADR-0046) and must NEVER become a Prometheus label
// (ADR-0032 cardinality).
func TestUsageMeters_TokensCharactersAudioSeconds(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.LLMTokens(ProviderGroq, "llama-3.3-70b-versatile", 100, 50)
	rec.TTSCharacters(ProviderElevenLabs, 42)
	rec.STTAudioSeconds(ProviderElevenLabs, 3200*time.Millisecond)

	out := scrape(t, rec)

	wantSeries := []string{
		`glyphoxa_voice_llm_tokens_total{direction="input",provider="groq"} 100`,
		`glyphoxa_voice_llm_tokens_total{direction="output",provider="groq"} 50`,
		`glyphoxa_voice_tts_characters_total{provider="elevenlabs"} 42`,
		`glyphoxa_voice_stt_audio_seconds_total{provider="elevenlabs"} 3.2`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q\n%s", want, filterGlyphoxa(out))
		}
	}

	// The model rides only to the spend meter; it must not reach a series (ADR-0032).
	if strings.Contains(out, "llama-3.3-70b-versatile") || strings.Contains(out, "model=") {
		t.Errorf("model leaked into a series (must never be a label):\n%s", filterGlyphoxa(out))
	}
}

// TestPlaybackGapAndLookaheadSeries is the #606 AC pin: the audible inter-sentence
// silence lands in its own label-free histogram (TurnID is NEVER a label, ADR-0032)
// with gap-sized bins, and the #375 look-ahead lane's three events are counted apart
// on a bounded event label.
func TestPlaybackGapAndLookaheadSeries(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.IntersentenceGap(400 * time.Millisecond)
	rec.PlaybackLookahead(LookaheadReleased)
	rec.PlaybackLookahead(LookaheadLatched)
	rec.PlaybackLookahead(LookaheadDiscarded)

	out := scrape(t, rec)

	wantSeries := []string{
		// 0.4s falls in the 0.5 bin, not the 0.35 one below it.
		`glyphoxa_voice_playback_intersentence_gap_seconds_bucket{le="0.35"} 0`,
		`glyphoxa_voice_playback_intersentence_gap_seconds_bucket{le="0.5"} 1`,
		`glyphoxa_voice_playback_intersentence_gap_seconds_count 1`,
		`glyphoxa_voice_playback_lookahead_total{event="released"} 1`,
		`glyphoxa_voice_playback_lookahead_total{event="latched"} 1`,
		`glyphoxa_voice_playback_lookahead_total{event="discarded"} 1`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q\n%s", want, filterGlyphoxa(out))
		}
	}
}

func TestSessionGaugeTracksOpenClose(t *testing.T) {
	rec := NewPrometheusRecorder()
	rec.SessionOpened("a")
	rec.SessionOpened("b")
	rec.SessionClosed("a")
	if got := scrape(t, rec); !strings.Contains(got, "glyphoxa_voice_sessions 1") {
		t.Fatalf("sessions gauge not at 1 after 2 open / 1 close:\n%s", filterGlyphoxa(got))
	}
}

// TestDBQueryHistogram pins the Postgres query-latency family (#605, ADR-0032):
// the metric name, the bounded `query` family label, and the sub-second bucket
// layout that makes an ANN p95 readable against the 250ms recall budget
// (ADR-0042).
func TestDBQueryHistogram(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.DBQuery("search_chunks", 5*time.Millisecond)
	rec.DBQuery("other", 300*time.Millisecond)

	out := scrape(t, rec)

	wantSubstrings := []string{
		`glyphoxa_db_query_seconds_bucket{query="search_chunks",le="0.005"} 1`,
		`glyphoxa_db_query_seconds_count{query="search_chunks"} 1`,
		`glyphoxa_db_query_seconds_bucket{query="other",le="0.25"} 0`,
		`glyphoxa_db_query_seconds_bucket{query="other",le="0.5"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

func TestNoUnboundedLabels(t *testing.T) {
	// Cardinality guard (ADR-0032 §2.1): the guild passed to the plumbing methods
	// must NEVER reach a series — only bounded enums label glyphoxa_voice_*.
	rec := NewPrometheusRecorder()
	rec.InboundFramesDropped("guild-SECRET-7788", 1)
	rec.ResponseLatency(RoleButler, time.Second)
	out := scrape(t, rec)
	for _, banned := range []string{"guild-SECRET-7788", "guild=", "agent_id=", "turn_id=", "tenant_id="} {
		if strings.Contains(out, banned) {
			t.Errorf("unbounded label leaked into series: %q\n%s", banned, filterGlyphoxa(out))
		}
	}
}

// TestMemoryRecallDurationHistogram pins the budgeted memory-recall duration
// series (#604, ADR-0042): one recall records BOTH the outcome counter and a
// duration observation on glyphoxa_voice_memory_recall_seconds, so a hit or miss
// drifting toward the hard 250ms budget is visible before it becomes a skip rate.
func TestMemoryRecallDurationHistogram(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.MemoryRecall(RecallMiss, 120*time.Millisecond)

	out := scrape(t, rec)

	wantSubstrings := []string{
		// 120ms lands in the 0.15 bin and every wider one, not in 0.1.
		`glyphoxa_voice_memory_recall_seconds_bucket{outcome="miss",le="0.1"} 0`,
		`glyphoxa_voice_memory_recall_seconds_bucket{outcome="miss",le="0.15"} 1`,
		`glyphoxa_voice_memory_recall_seconds_bucket{outcome="miss",le="0.25"} 1`,
		`glyphoxa_voice_memory_recall_seconds_count{outcome="miss"} 1`,
		// The counter is unchanged: duration is additive, not a replacement.
		`glyphoxa_voice_memory_recall_total{outcome="miss"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

// TestKGFactsDurationHistogram pins the budgeted KG-fact-read duration series
// (#604): a degraded read records its duration on glyphoxa_kg_facts_seconds, so a
// degradation can be told apart as a budget overrun rather than a fast DB error.
// Process-level namespace (glyphoxa_, no voice subsystem), matching kg_facts_total.
func TestKGFactsDurationHistogram(t *testing.T) {
	rec := NewPrometheusRecorder()

	rec.KGFacts(FactsDegraded, 60*time.Millisecond)

	out := scrape(t, rec)

	wantSubstrings := []string{
		// 60ms overran the 50ms budget: it misses the 0.05 bin, lands in 0.1.
		`glyphoxa_kg_facts_seconds_bucket{outcome="degraded",le="0.05"} 0`,
		`glyphoxa_kg_facts_seconds_bucket{outcome="degraded",le="0.1"} 1`,
		`glyphoxa_kg_facts_seconds_count{outcome="degraded"} 1`,
		`glyphoxa_kg_facts_total{outcome="degraded"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

func filterGlyphoxa(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "glyphoxa") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
