package app

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/transcript"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// audioPipelineConfig holds all dependencies for an [audioPipeline].
type audioPipelineConfig struct {
	conn        audio.Connection
	vadEngine   vad.Engine
	sttProvider stt.Provider
	orch        *orchestrator.Orchestrator
	mixer       audio.Mixer
	vadCfg      vad.Config
	sttCfg      stt.StreamConfig
	ctx         context.Context
	pipeline    transcript.Pipeline // may be nil — correction is skipped when nil
	entities    func() []string     // returns current entity names; may be nil
}

// audioPipeline manages per-participant audio processing goroutines.
// It reads PCM from input streams, runs VAD to detect speech segments,
// pipes speech audio to STT, and routes final transcripts to NPC agents
// via the orchestrator.
//
// All exported methods are safe for concurrent use.
type audioPipeline struct {
	conn        audio.Connection
	vadEngine   vad.Engine
	sttProvider stt.Provider
	orch        *orchestrator.Orchestrator
	mixer       audio.Mixer
	vadCfg      vad.Config
	sttCfg      stt.StreamConfig
	pipeline    transcript.Pipeline
	entities    func() []string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	workers map[string]context.CancelFunc
}

// newAudioPipeline creates an audioPipeline from the given config.
// Call Start to begin processing and Stop to tear down.
func newAudioPipeline(cfg audioPipelineConfig) *audioPipeline {
	ctx, cancel := context.WithCancel(cfg.ctx)
	return &audioPipeline{
		conn:        cfg.conn,
		vadEngine:   cfg.vadEngine,
		sttProvider: cfg.sttProvider,
		orch:        cfg.orch,
		mixer:       cfg.mixer,
		vadCfg:      cfg.vadCfg,
		sttCfg:      cfg.sttCfg,
		pipeline:    cfg.pipeline,
		entities:    cfg.entities,
		ctx:         ctx,
		cancel:      cancel,
		workers:     make(map[string]context.CancelFunc),
	}
}

// UpdateKeywords atomically replaces the keyword boost list used when opening
// new STT sessions. Already-active sessions are unaffected (they are short-lived
// per speech segment and will pick up the new keywords on the next VAD cycle).
func (p *audioPipeline) UpdateKeywords(keywords []stt.KeywordBoost) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sttCfg.Keywords = keywords
}

// Start registers a participant change callback and starts workers
// for any participants already in the channel.
func (p *audioPipeline) Start() {
	p.conn.OnParticipantChange(p.handleParticipantChange)

	for id, ch := range p.conn.InputStreams() {
		p.startWorker(id, ch)
	}

	slog.Info("audio pipeline: started")
}

// Stop cancels all workers and waits for them to finish.
// It satisfies the func() error closer pattern.
func (p *audioPipeline) Stop() error {
	p.cancel()
	p.wg.Wait()
	slog.Info("audio pipeline: stopped")
	return nil
}

// handleParticipantChange is the callback for join/leave events.
func (p *audioPipeline) handleParticipantChange(ev audio.Event) {
	switch ev.Type {
	case audio.EventJoin:
		streams := p.conn.InputStreams()
		ch, ok := streams[ev.UserID]
		if !ok {
			slog.Warn("audio pipeline: join event but no input stream",
				"user_id", ev.UserID, "username", ev.Username)
			return
		}
		p.startWorker(ev.UserID, ch)

	case audio.EventLeave:
		p.mu.Lock()
		if cancelFn, ok := p.workers[ev.UserID]; ok {
			cancelFn()
			delete(p.workers, ev.UserID)
		}
		p.mu.Unlock()
		slog.Debug("audio pipeline: participant left",
			"user_id", ev.UserID, "username", ev.Username)
	}
}

// startWorker launches a per-participant processing goroutine that converts
// the input stream to 16kHz mono, runs VAD, and pipes speech to STT.
func (p *audioPipeline) startWorker(id string, ch <-chan audio.AudioFrame) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.workers[id]; exists {
		return
	}

	workerCtx, workerCancel := context.WithCancel(p.ctx)
	p.workers[id] = workerCancel

	converted := audio.ConvertStream(ch, audio.Format{
		SampleRate: p.vadCfg.SampleRate,
		Channels:   1,
	})

	p.wg.Go(func() {
		p.processParticipant(workerCtx, id, converted)
	})

	slog.Info("audio pipeline: started worker", "user_id", id)
}

// processParticipant runs the VAD → STT → agent pipeline for a single
// participant. It accumulates converted PCM into VAD-sized frames, detects
// speech boundaries, and opens/closes STT sessions accordingly.
func (p *audioPipeline) processParticipant(ctx context.Context, speakerID string, frames <-chan audio.AudioFrame) {
	vadSession, err := p.vadEngine.NewSession(p.vadCfg)
	if err != nil {
		slog.Error("audio pipeline: create VAD session",
			"speaker", speakerID, "err", err)
		return
	}
	defer vadSession.Close()

	// VAD frame size in bytes: samples_per_frame * 2 bytes per int16 sample.
	vadFrameBytes := p.vadCfg.SampleRate * p.vadCfg.FrameSizeMs / 1000 * 2
	var buf []byte

	var sttSession stt.SessionHandle

	defer func() {
		if sttSession != nil {
			_ = sttSession.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}

			buf = append(buf, frame.Data...)

			for len(buf) >= vadFrameBytes {
				vadFrame := buf[:vadFrameBytes]
				buf = buf[vadFrameBytes:]

				event, vadErr := vadSession.ProcessFrame(vadFrame)
				if vadErr != nil {
					slog.Warn("audio pipeline: VAD process frame",
						"speaker", speakerID, "err", vadErr)
					continue
				}

				switch event.Type {
				case vad.VADSpeechStart:
					slog.Debug("audio pipeline: speech start",
						"speaker", speakerID, "prob", event.Probability)

					// Barge-in: interrupt current NPC playback and clear queue.
					p.mixer.Interrupt(audio.PlayerBargeIn)

					// Snapshot STT config under lock — UpdateKeywords may be
					// writing p.sttCfg.Keywords concurrently.
					p.mu.Lock()
					cfg := p.sttCfg
					cfg.Keywords = slices.Clone(p.sttCfg.Keywords)
					p.mu.Unlock()

					// Open a new STT session.
					sttSession, err = p.sttProvider.StartStream(ctx, cfg)
					if err != nil {
						slog.Error("audio pipeline: start STT stream",
							"speaker", speakerID, "err", err)
						sttSession = nil
						continue
					}

					// Start collector goroutine to drain Finals concurrently.
					p.wg.Add(1)
					go func(s stt.SessionHandle) {
						defer p.wg.Done()
						p.collectAndRoute(ctx, speakerID, s)
					}(sttSession)

					// Feed the triggering frame to STT.
					if sendErr := sttSession.SendAudio(vadFrame); sendErr != nil {
						slog.Warn("audio pipeline: send audio to STT",
							"speaker", speakerID, "err", sendErr)
					}

				case vad.VADSpeechContinue:
					if sttSession != nil {
						if sendErr := sttSession.SendAudio(vadFrame); sendErr != nil {
							slog.Warn("audio pipeline: send audio to STT",
								"speaker", speakerID, "err", sendErr)
						}
					}

				case vad.VADSpeechEnd:
					slog.Debug("audio pipeline: speech end",
						"speaker", speakerID)
					if sttSession != nil {
						_ = sttSession.Close()
						sttSession = nil
					}

				case vad.VADSilence:
					// No-op.
				}
			}
		}
	}
}

// collectAndRoute drains final transcripts from an STT session, optionally
// applies transcript correction, and routes each to the appropriate NPC agent
// via the orchestrator.
//
// The function exits when ctx is cancelled or session.Finals() is closed,
// whichever comes first. This ensures the goroutine does not leak if the STT
// provider fails to close its channel on context cancellation.
func (p *audioPipeline) collectAndRoute(ctx context.Context, speakerID string, session stt.SessionHandle) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-session.Finals():
			if !ok {
				return
			}

			if strings.TrimSpace(t.Text) == "" {
				continue
			}

			// Apply transcript correction if pipeline and entities are available.
			if p.pipeline != nil && p.entities != nil {
				entities := p.entities()
				if len(entities) > 0 {
					corrected, err := p.pipeline.Correct(ctx, t, entities)
					if err != nil {
						slog.Warn("audio pipeline: transcript correction failed, using raw",
							"speaker", speakerID, "err", err)
					} else if corrected.Corrected != t.Text {
						slog.Info("audio pipeline: transcript corrected",
							"speaker", speakerID,
							"raw", t.Text,
							"corrected", corrected.Corrected,
							"corrections", len(corrected.Corrections),
						)
						t.Text = corrected.Corrected
					}
				}
			}

			slog.Info("audio pipeline: transcript",
				"speaker", speakerID,
				"text", t.Text,
				"confidence", t.Confidence,
			)

			target, err := p.orch.Route(ctx, speakerID, t)
			if err != nil {
				slog.Debug("audio pipeline: route transcript",
					"speaker", speakerID, "err", err)
				continue
			}

			if err := target.HandleUtterance(ctx, speakerID, t); err != nil {
				slog.Error("audio pipeline: handle utterance",
					"speaker", speakerID, "npc", target.Name(), "err", err)
			}
		}
	}
}
