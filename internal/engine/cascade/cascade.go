// Package cascade implements an experimental dual-model sentence cascade engine.
//
// The cascade reduces perceived voice latency by starting TTS playback with a
// fast model's opening sentence while a stronger model generates the substantive
// continuation. The two outputs are stitched into a single seamless audio stream.
//
// # Architecture
//
//  1. Player finishes speaking → STT finalises transcript.
//  2. Fast model (e.g., GPT-4o-mini, Gemini Flash) generates only the first
//     sentence (~200 ms TTFT).
//  3. TTS starts immediately on the first sentence.
//  4. Strong model (e.g., Claude Sonnet, GPT-4o) receives the same prompt plus
//     the fast model's first sentence as a forced continuation prefix.
//  5. TTS continues with the strong model's output → seamless single utterance.
//
// This is opt-in per NPC via the cascade_mode configuration field and is not
// recommended for simple greetings or combat callouts where a single fast model
// suffices.
package cascade

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/internal/engine"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

const (
	// defaultOpenerSuffix is the instruction appended to the fast model's system
	// prompt to constrain it to a brief, in-character opening reaction.
	defaultOpenerSuffix = "Generate only a brief, in-character opening reaction. Do not reveal key information in the first sentence."

	// defaultTranscriptBuf is the default buffer depth of the transcript channel.
	defaultTranscriptBuf = 32

	// defaultTextBuf is the buffer depth of the text channel passed to TTS in the
	// dual-model path. Sized to absorb the opener plus several strong-model sentences
	// without blocking the synthesis goroutine.
	defaultTextBuf = 16

	// defaultMaxToolIters is the maximum number of tool-call loop iterations
	// before the cascade engine gives up and flushes accumulated text.
	defaultMaxToolIters = 5
)

// Engine implements [engine.VoiceEngine] using a dual-model sentence cascade.
//
// A fast LLM produces the NPC's opening sentence immediately so TTS can start
// playing within ~500 ms. A strong LLM then generates the continuation, receiving
// the opener as a forced assistant-role prefix so the response sounds seamless.
//
// Engine is safe for concurrent use. Multiple concurrent [Engine.Process] calls
// are allowed; each spawns an independent goroutine for the strong-model stage.
type Engine struct {
	fastLLM   llm.Provider
	strongLLM llm.Provider
	ttsP      tts.Provider
	voice     tts.VoiceProfile
	sttP      stt.Provider // nil = text-only mode (STT skipped)

	openerSuffix  string
	transcriptBuf int

	// ttsSampleRate is the sample rate in Hz of PCM audio produced by the TTS
	// provider (e.g., 22050 for Coqui XTTS, 16000 for ElevenLabs). Defaults to
	// 22050 if not set via [WithTTSFormat].
	ttsSampleRate int

	// ttsChannels is the number of audio channels produced by the TTS provider
	// (1 = mono, 2 = stereo). Defaults to 1 if not set via [WithTTSFormat].
	ttsChannels int

	mu            sync.Mutex
	toolHandler   func(name, args string) (string, error)
	tools         []llm.ToolDefinition
	pendingUpdate *engine.ContextUpdate
	transcriptCh  chan memory.TranscriptEntry
	done          chan struct{}
	closed        bool

	// maxToolIters caps how many tool-call round-trips the strong model is
	// allowed during a single Process call. Defaults to defaultMaxToolIters.
	maxToolIters int

	// wg tracks background goroutines spawned by Process so callers (and tests)
	// can synchronise with the end of the strong-model stage.
	wg sync.WaitGroup
}

// Compile-time assertion that Engine satisfies the engine.VoiceEngine interface.
var _ engine.VoiceEngine = (*Engine)(nil)

// Option is a functional option for configuring an Engine during construction.
type Option func(*Engine)

// WithSTT configures an STT provider for audio input processing.
// When set, [Engine.Process] will transcribe audio frames before LLM generation.
// If nil, audio input is ignored and text from the PromptContext is used directly.
func WithSTT(s stt.Provider) Option {
	return func(e *Engine) { e.sttP = s }
}

// WithTranscriptBuffer sets the buffer capacity of the transcript channel
// returned by [Engine.Transcripts]. Default is 32.
func WithTranscriptBuffer(n int) Option {
	return func(e *Engine) { e.transcriptBuf = n }
}

// WithOpenerPromptSuffix overrides the instruction appended to the fast model's
// system prompt. The default instructs the model to generate only a brief,
// in-character opening reaction without revealing key information.
func WithOpenerPromptSuffix(s string) Option {
	return func(e *Engine) { e.openerSuffix = s }
}

// WithMaxToolIterations sets the maximum number of tool-call loop iterations
// before the cascade gives up and flushes accumulated text. Defaults to 5.
func WithMaxToolIterations(n int) Option {
	return func(e *Engine) { e.maxToolIters = n }
}

// WithTTSFormat sets the expected TTS output format for the audio pipeline.
// sampleRate is in Hz (e.g., 22050 for Coqui XTTS, 16000 for ElevenLabs).
// channels is the number of audio channels (1 = mono, 2 = stereo).
// If not called, defaults are 22050 Hz mono.
func WithTTSFormat(sampleRate, channels int) Option {
	return func(e *Engine) {
		e.ttsSampleRate = sampleRate
		e.ttsChannels = channels
	}
}

// New constructs a cascade Engine backed by the given providers and voice profile.
// Options are applied after the engine is initialised with its defaults.
func New(fastLLM, strongLLM llm.Provider, ttsP tts.Provider, voice tts.VoiceProfile, opts ...Option) *Engine {
	e := &Engine{
		fastLLM:       fastLLM,
		strongLLM:     strongLLM,
		ttsP:          ttsP,
		voice:         voice,
		openerSuffix:  defaultOpenerSuffix,
		transcriptBuf: defaultTranscriptBuf,
		done:          make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	// Apply defaults for TTS format and tool iterations if not set by options.
	if e.ttsSampleRate == 0 {
		e.ttsSampleRate = 22050
	}
	if e.ttsChannels == 0 {
		e.ttsChannels = 1
	}
	if e.maxToolIters == 0 {
		e.maxToolIters = defaultMaxToolIters
	}
	// Create transcript channel after options so WithTranscriptBuffer takes effect.
	e.transcriptCh = make(chan memory.TranscriptEntry, e.transcriptBuf)
	return e
}

// ─── VoiceEngine interface ────────────────────────────────────────────────────

// Process handles a complete voice interaction using the dual-model sentence cascade.
//
// It applies any pending [engine.ContextUpdate] from a prior [Engine.InjectContext]
// call, then:
//  1. Sends the prompt to the fast model with an opener instruction.
//  2. Collects the first sentence of the fast model's reply.
//  3. If the fast model's response is a single sentence, synthesises it directly
//     (single-model path — no strong model involved).
//  4. Otherwise, begins TTS on the opener immediately and in a background goroutine
//     calls the strong model with the opener as a forced assistant-role continuation
//     prefix, forwarding its output to the same TTS stream.
//
// The returned [engine.Response] is available as soon as TTS synthesis starts;
// audio continues streaming after Process returns.
func (e *Engine) Process(ctx context.Context, _ audio.AudioFrame, prompt engine.PromptContext) (*engine.Response, error) {
	// Apply and consume any pending context update atomically.
	e.mu.Lock()
	if e.pendingUpdate != nil {
		prompt = mergeContextUpdate(prompt, *e.pendingUpdate)
		e.pendingUpdate = nil
	}
	tools := make([]llm.ToolDefinition, len(e.tools))
	copy(tools, e.tools)
	e.mu.Unlock()

	// ── Stage 1: Fast model → opener ─────────────────────────────────────────

	fastReq := e.buildFastPrompt(prompt)
	fastCh, err := e.fastLLM.StreamCompletion(ctx, fastReq)
	if err != nil {
		return nil, fmt.Errorf("cascade: fast model stream failed: %w", err)
	}

	opener, fastFull, streamErr := e.collectFirstSentence(ctx, fastCh)
	if streamErr != nil {
		return nil, streamErr
	}
	if opener == "" {
		opener = "..." // guard: prevent silent TTS on empty opener
	}

	// ── Stage 2a: Single-model path (fast model was complete in one sentence) ─

	if fastFull {
		textCh := make(chan string, 1)
		textCh <- opener
		close(textCh)

		audioCh, err := e.ttsP.SynthesizeStream(ctx, textCh, e.voice)
		if err != nil {
			return nil, fmt.Errorf("cascade: TTS start failed: %w", err)
		}

		// Emit player input transcript entry.
		if playerMsg, ok := lastUserMessage(prompt.Messages); ok {
			e.emitTranscript(memory.TranscriptEntry{
				SpeakerID:   playerMsg.Name,
				SpeakerName: playerMsg.Name,
				Text:        playerMsg.Content,
				Timestamp:   time.Now(),
			})
		}

		// Emit NPC response transcript entry.
		e.emitTranscript(memory.TranscriptEntry{
			Text:      opener,
			Timestamp: time.Now(),
		})

		return &engine.Response{Text: opener, Audio: audioCh, SampleRate: e.ttsSampleRate, Channels: e.ttsChannels}, nil
	}

	// ── Stage 2b: Dual-model path ─────────────────────────────────────────────

	// Create the shared text channel that feeds the TTS stream.
	textCh := make(chan string, defaultTextBuf)
	audioCh, err := e.ttsP.SynthesizeStream(ctx, textCh, e.voice)
	if err != nil {
		return nil, fmt.Errorf("cascade: TTS start failed: %w", err)
	}

	strongReq := e.buildStrongPrompt(prompt, tools, opener)
	resp := &engine.Response{Text: opener, Audio: audioCh, SampleRate: e.ttsSampleRate, Channels: e.ttsChannels}

	// Background goroutine: send opener → strong model (with tool loop) → close textCh.
	e.wg.Go(func() {
		defer close(textCh)

		// Deliver the opener to TTS immediately so playback begins.
		select {
		case textCh <- opener:
		case <-ctx.Done():
			return
		}

		// Run the strong model with tool-call loop support.
		continuation := e.forwardStrongModel(ctx, strongReq, textCh, resp)
		fullText := opener
		if continuation != "" {
			fullText = opener + " " + continuation
		}

		// Emit player input transcript entry.
		if playerMsg, ok := lastUserMessage(prompt.Messages); ok {
			e.emitTranscript(memory.TranscriptEntry{
				SpeakerID:   playerMsg.Name,
				SpeakerName: playerMsg.Name,
				Text:        playerMsg.Content,
				Timestamp:   time.Now(),
			})
		}

		// Emit NPC response transcript entry.
		e.emitTranscript(memory.TranscriptEntry{
			Text:      strings.TrimSpace(fullText),
			Timestamp: time.Now(),
		})
	})

	return resp, nil
}

// InjectContext queues a context update to be merged on the next [Engine.Process]
// call. It is non-blocking and safe to call concurrently.
func (e *Engine) InjectContext(_ context.Context, update engine.ContextUpdate) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingUpdate = &update
	return nil
}

// SetTools replaces the tool set offered to the strong model on the next
// [Engine.Process] call. The fast model never receives tools.
// Pass a nil or empty slice to disable tool calling.
func (e *Engine) SetTools(tools []llm.ToolDefinition) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(tools) == 0 {
		e.tools = nil
		return nil
	}
	cp := make([]llm.ToolDefinition, len(tools))
	copy(cp, tools)
	e.tools = cp
	return nil
}

// OnToolCall registers handler as the executor for LLM tool calls issued by the
// strong model. Only the most recently registered handler is active.
func (e *Engine) OnToolCall(handler func(name string, args string) (string, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.toolHandler = handler
}

// Transcripts returns a read-only channel that emits [memory.TranscriptEntry]
// values. The channel is closed when the engine is closed.
//
// The returned channel is the same value for the lifetime of the engine —
// it is assigned once in [New] and never mutated — so no lock is required.
func (e *Engine) Transcripts() <-chan memory.TranscriptEntry {
	return e.transcriptCh
}

// Close releases all resources held by the engine and closes the Transcripts
// channel. Close is safe to call multiple times; subsequent calls return nil.
//
// Close waits for all background goroutines spawned by [Engine.Process] to
// finish before closing the transcript channel, preventing writes to a closed
// channel.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	close(e.done)
	e.mu.Unlock()

	// Wait for in-flight Process goroutines before closing the channel.
	e.wg.Wait()
	close(e.transcriptCh)
	return nil
}

// Wait blocks until all background goroutines spawned by [Engine.Process] have
// finished. This is primarily useful in tests to synchronise before inspecting
// mock call records.
func (e *Engine) Wait() {
	e.wg.Wait()
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// buildFastPrompt constructs the [llm.CompletionRequest] for the fast model.
// It appends the opener instruction to the system prompt and excludes tools so
// the fast model stays fast and on-topic.
func (e *Engine) buildFastPrompt(prompt engine.PromptContext) llm.CompletionRequest {
	var sb strings.Builder
	sb.WriteString(prompt.SystemPrompt)
	if prompt.HotContext != "" {
		sb.WriteString("\n\n")
		sb.WriteString(prompt.HotContext)
	}
	if e.openerSuffix != "" {
		sb.WriteString("\n\n")
		sb.WriteString(e.openerSuffix)
	}

	msgs := make([]llm.Message, len(prompt.Messages))
	copy(msgs, prompt.Messages)

	return llm.CompletionRequest{
		SystemPrompt: sb.String(),
		Messages:     msgs,
		// Tools intentionally omitted: fast model does not use tools.
	}
}

// buildStrongPrompt constructs the [llm.CompletionRequest] for the strong model.
// It injects the fast model's opener as a forced assistant-role continuation
// prefix so the strong model generates a seamless continuation.
func (e *Engine) buildStrongPrompt(prompt engine.PromptContext, tools []llm.ToolDefinition, opener string) llm.CompletionRequest {
	var sb strings.Builder
	sb.WriteString(prompt.SystemPrompt)
	if prompt.HotContext != "" {
		sb.WriteString("\n\n")
		sb.WriteString(prompt.HotContext)
	}

	// Append existing messages then inject the opener as an assistant prefix.
	msgs := make([]llm.Message, len(prompt.Messages)+1)
	copy(msgs, prompt.Messages)
	msgs[len(prompt.Messages)] = llm.Message{
		Role:    "assistant",
		Content: opener,
	}

	return llm.CompletionRequest{
		SystemPrompt: sb.String(),
		Messages:     msgs,
		Tools:        tools,
	}
}

// collectFirstSentence reads token chunks from ch and returns the first complete
// sentence — defined as text ending with '.', '!', or '?' immediately followed by
// a whitespace character. If the stream ends before a sentence boundary is
// detected, the entire accumulated text is returned with full=true (meaning the
// fast model's response was one sentence or fewer, so the strong model is
// unnecessary).
//
// When full is false, remaining chunks in ch are drained in a background goroutine
// to prevent the provider's goroutine from leaking.
func (e *Engine) collectFirstSentence(ctx context.Context, ch <-chan llm.Chunk) (sentence string, full bool, streamErr error) {
	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			return buf.String(), true, ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				// Channel closed without a finish-reason chunk.
				return buf.String(), true, nil
			}

			// LLM provider error — do not pass the error text to TTS.
			if chunk.FinishReason == "error" {
				go drainChunks(ch)
				return "", true, fmt.Errorf("cascade: LLM stream error: %s", chunk.Text)
			}

			buf.WriteString(chunk.Text)

			// A finish-reason marks the end of the stream — the entire
			// response fits in this buffer, so no strong model is needed.
			if chunk.FinishReason != "" {
				return buf.String(), true, nil
			}

			// Look for a sentence boundary only while the stream is live.
			s := buf.String()
			if idx := firstSentenceBoundary(s); idx >= 0 {
				// Drain remaining fast-model output to avoid goroutine leaks.
				go drainChunks(ch)
				return s[:idx+1], false, nil
			}
		}
	}
}

// forwardStrongModel runs the strong model, forwarding text to TTS and handling
// tool calls in a loop. It returns the full concatenation of all text emitted
// across all iterations.
func (e *Engine) forwardStrongModel(
	ctx context.Context,
	strongReq llm.CompletionRequest,
	textCh chan<- string,
	resp *engine.Response,
) string {
	var fullText strings.Builder

	for iter := range e.maxToolIters {
		ch, err := e.strongLLM.StreamCompletion(ctx, strongReq)
		if err != nil {
			resp.SetStreamErr(fmt.Errorf("cascade: strong model stream (iter %d): %w", iter, err))
			return fullText.String()
		}

		text, toolCalls, finished := e.forwardSentencesWithTools(ctx, ch, textCh)
		fullText.WriteString(text)

		if finished || len(toolCalls) == 0 {
			return fullText.String()
		}

		// Read the tool handler under lock, then release before executing.
		e.mu.Lock()
		handler := e.toolHandler
		e.mu.Unlock()

		if handler == nil {
			slog.Warn("cascade: tool calls requested but no handler registered",
				"tool_count", len(toolCalls), "iter", iter)
			return fullText.String()
		}

		// Build the assistant message that requested tools (required by LLM APIs).
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   text,
			ToolCalls: toolCalls,
		}

		// Execute each tool call and build result messages.
		var toolResults []llm.Message
		for _, tc := range toolCalls {
			slog.Debug("cascade: executing tool call",
				"tool", tc.Name, "iter", iter)
			result, toolErr := handler(tc.Name, tc.Arguments)
			if toolErr != nil {
				slog.Debug("cascade: tool call failed",
					"tool", tc.Name, "error", toolErr)
				result = fmt.Sprintf("Tool error: %s", toolErr.Error())
			}
			toolResults = append(toolResults, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// Rebuild the request with tool results appended.
		strongReq.Messages = append(strongReq.Messages, assistantMsg)
		strongReq.Messages = append(strongReq.Messages, toolResults...)
	}

	slog.Warn("cascade: tool call iteration cap reached", "max", e.maxToolIters)
	return fullText.String()
}

// forwardSentencesWithTools reads token chunks from ch, accumulates them into
// complete sentences, and writes each sentence to textCh for TTS. It also
// accumulates tool calls across chunks (streaming providers may split arguments
// across multiple chunks).
//
// Returns:
//   - text: the full concatenation of all text chunks received.
//   - toolCalls: accumulated tool calls, non-empty only when FinishReason is "tool_calls".
//   - finished: true when the stream completed normally without pending tool calls.
func (e *Engine) forwardSentencesWithTools(
	ctx context.Context,
	ch <-chan llm.Chunk,
	textCh chan<- string,
) (text string, toolCalls []llm.ToolCall, finished bool) {
	var buf, collected strings.Builder
	var accum []llm.ToolCall

	for {
		select {
		case <-ctx.Done():
			return collected.String(), nil, true
		case chunk, ok := <-ch:
			if !ok {
				// Channel closed: flush remaining text.
				if buf.Len() > 0 {
					select {
					case textCh <- buf.String():
					case <-ctx.Done():
					}
				}
				return collected.String(), nil, true
			}

			// Accumulate tool calls across chunks.
			if len(chunk.ToolCalls) > 0 {
				accum = accumulateToolCalls(accum, chunk.ToolCalls)
			}

			if chunk.Text != "" {
				buf.WriteString(chunk.Text)
				collected.WriteString(chunk.Text)
			}

			// Flush complete sentences eagerly for lower TTS latency.
			for {
				s := buf.String()
				idx := firstSentenceBoundary(s)
				if idx < 0 {
					break
				}
				sentence := s[:idx+1]
				rest := s[idx+1:]
				buf.Reset()
				buf.WriteString(strings.TrimLeft(rest, " \t\n\r"))
				select {
				case textCh <- sentence:
				case <-ctx.Done():
					return collected.String(), nil, true
				}
			}

			// On the final chunk, flush any remaining partial sentence.
			if chunk.FinishReason != "" {
				// LLM provider error — discard buffered error text.
				if chunk.FinishReason == "error" {
					return collected.String(), nil, true
				}
				if buf.Len() > 0 {
					select {
					case textCh <- buf.String():
					case <-ctx.Done():
					}
				}
				if chunk.FinishReason == "tool_calls" && len(accum) > 0 {
					return collected.String(), accum, false
				}
				return collected.String(), nil, true
			}
		}
	}
}

// accumulateToolCalls merges incremental ToolCall chunks into complete calls.
// Streaming providers (e.g., OpenAI) send partial Arguments strings across
// chunks at the same index position. This function concatenates Arguments by
// index and prefers non-empty ID/Name fields from later chunks.
func accumulateToolCalls(existing []llm.ToolCall, incoming []llm.ToolCall) []llm.ToolCall {
	for i, tc := range incoming {
		if i < len(existing) {
			if tc.ID != "" {
				existing[i].ID = tc.ID
			}
			if tc.Name != "" {
				existing[i].Name = tc.Name
			}
			existing[i].Arguments += tc.Arguments
		} else {
			existing = append(existing, tc)
		}
	}
	return existing
}

// firstSentenceBoundary returns the index of the first sentence-ending
// punctuation ('.', '!', or '?') followed by whitespace, skipping common false
// positives:
//
//   - Abbreviations: single uppercase letter + period ("A. Smith")
//   - Common title abbreviations: "Dr.", "Mr.", "Mrs.", "Ms.", "St.", etc.
//   - Decimal numbers: digit + period + digit ("2.5 gold")
//   - Ellipses: ASCII "..." or Unicode '\u2026'
//
// Returns -1 if no boundary exists in s.
func firstSentenceBoundary(s string) int {
	n := len(s)
	for i := 0; i < n-1; i++ {
		switch s[i] {
		case '.':
			if !isSentenceWhitespace(s[i+1]) {
				continue
			}
			if isEllipsisDot(s, i) {
				continue
			}
			if isDecimalDot(s, i) {
				continue
			}
			if isAbbreviationDot(s, i) {
				continue
			}
			return i
		case '!', '?':
			if !isSentenceWhitespace(s[i+1]) {
				continue
			}
			return i
		case 0xE2: // First byte of Unicode ellipsis '…' (U+2026: 0xE2 0x80 0xA6).
			if i+2 < n && s[i+1] == 0x80 && s[i+2] == 0xA6 {
				i += 2 // Skip the remaining bytes of the 3-byte sequence.
			}
		}
	}
	return -1
}

// isSentenceWhitespace reports whether b is a whitespace byte that follows
// sentence-ending punctuation.
func isSentenceWhitespace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

// isEllipsisDot reports whether the period at index i is part of an ASCII
// ellipsis ("..."). A period adjacent to another period is always part of an
// ellipsis.
func isEllipsisDot(s string, i int) bool {
	if i >= 1 && s[i-1] == '.' {
		return true
	}
	if i+1 < len(s) && s[i+1] == '.' {
		return true
	}
	return false
}

// isDecimalDot reports whether the period at index i sits between two ASCII
// digits (e.g., "2.5").
func isDecimalDot(s string, i int) bool {
	if i == 0 || i+1 >= len(s) {
		return false
	}
	return s[i-1] >= '0' && s[i-1] <= '9' && s[i+1] >= '0' && s[i+1] <= '9'
}

// isAbbreviationDot reports whether the period at index i follows a likely
// abbreviation:
//
//   - A single uppercase letter: "A. Smith", "J. R. R. Tolkien"
//   - A common title or military abbreviation: "Dr.", "Mr.", "Lt.", etc.
func isAbbreviationDot(s string, i int) bool {
	if i == 0 {
		return false
	}

	// Single uppercase letter: "A." at string start or after whitespace.
	if i == 1 || (i >= 2 && (s[i-2] == ' ' || s[i-2] == '\n')) {
		c := s[i-1]
		if c >= 'A' && c <= 'Z' {
			return true
		}
	}

	// Scan backwards to the word start and check against known abbreviations.
	wordStart := i - 1
	for wordStart > 0 && s[wordStart-1] != ' ' && s[wordStart-1] != '\n' {
		wordStart--
	}
	word := strings.ToLower(s[wordStart:i])

	switch word {
	case "dr", "mr", "mrs", "ms", "st", "jr", "sr", "vs",
		"lt", "cpt", "cmdr", "prof", "sgt", "gen", "col":
		return true
	}
	return false
}

// drainChunks discards all remaining chunks from ch. Used to prevent the LLM
// provider's internal goroutine from blocking when collectFirstSentence returns
// before the stream is exhausted.
func drainChunks(ch <-chan llm.Chunk) {
	for range ch {
	}
}

// emitTranscript sends a TranscriptEntry to the transcript channel if the
// engine is still open. It is safe to call concurrently.
func (e *Engine) emitTranscript(entry memory.TranscriptEntry) {
	select {
	case e.transcriptCh <- entry:
	case <-e.done:
	}
}

// lastUserMessage returns the last user-role message from the conversation
// history, if one exists.
func lastUserMessage(msgs []llm.Message) (llm.Message, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i], true
		}
	}
	return llm.Message{}, false
}

// mergeContextUpdate applies a [engine.ContextUpdate] onto a [engine.PromptContext],
// returning the merged result. Zero-value fields in update are ignored.
func mergeContextUpdate(prompt engine.PromptContext, update engine.ContextUpdate) engine.PromptContext {
	if update.Identity != "" {
		prompt.SystemPrompt = update.Identity
	}
	if update.Scene != "" {
		prompt.HotContext = update.Scene
	}
	if len(update.RecentUtterances) > 0 {
		extra := make([]llm.Message, len(update.RecentUtterances))
		for i, u := range update.RecentUtterances {
			role := "user"
			if u.IsNPC() {
				role = "assistant"
			}
			extra[i] = llm.Message{
				Role:    role,
				Content: u.Text,
				Name:    u.SpeakerName,
			}
		}
		msgs := make([]llm.Message, len(prompt.Messages)+len(extra))
		copy(msgs, prompt.Messages)
		copy(msgs[len(prompt.Messages):], extra)
		prompt.Messages = msgs
	}
	return prompt
}
