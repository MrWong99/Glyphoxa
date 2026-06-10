package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// stubRecognizer is a [stt.Recognizer] that returns a pinned [stt.Transcript]
// regardless of input. Used to exercise the orchestrator stage's republish
// contract independently of any real provider or cassette.
type stubRecognizer struct {
	transcript stt.Transcript
}

func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return s.transcript, nil
}

// TestSTT_HelloTest_EmitsFinal is TB5: the first cassette-backed tracer
// bullet (ADR-0021). It feeds the "hello-test" clip through the orchestrator
// STT stage backed by a replay-only [voicecassette.STTRecognizer] and
// asserts that exactly the recorded transcript reaches the shared bus as a
// [voiceevent.STTFinal] (ADR-0020).
//
// The cassette under tests/voice-cassettes/stt-hello-test.yaml pins both the
// audio fingerprint and the transcript text; re-recording happens under
// `-tags=record` against the live ElevenLabs scribe_v2 adapter (ADR-0021).
// The assertion compares the bus event against the clip's known utterance
// after normalization (see [voicetest.NormalizeTranscript]) so it pins the
// transcribed words while tolerating provider-specific casing, spacing, and
// punctuation — scribe_v2 drops this utterance's trailing period, which an
// exact-match assertion would (and did) flag as a spurious failure.
func TestSTT_HelloTest_EmitsFinal(t *testing.T) {
	h := voicetest.New(t)

	clip := voicetest.LoadClip(t, "hello-test")
	const frameMs = 32 // mirrors the VAD stage; any consistent framing works
	frames, tail := clip.FramesOf(t, clip.SampleRate*frameMs/1000)
	if tail != 0 {
		t.Logf("hello-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/clip.SampleRate)
	}

	recognizer := voicecassette.LoadSTT(t, "stt-hello-test")
	stage := orchestrator.NewSTT(h.Bus, recognizer)

	if err := stage.Transcribe(context.Background(), frames); err != nil {
		t.Fatalf("stage.Transcribe: %v", err)
	}

	want := voicetest.NormalizeTranscript(helloUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.STTFinal) bool { return voicetest.NormalizeTranscript(e.Text) == want },
		"stt.final matching utterance "+helloUtterance,
	)
}

// TestSTT_TTRPGIntro_TranscribesBothLanguages drives the full slice-1 voice
// pipeline — audio → VAD → STT → address detection → TTS — through the
// [orchestrator.Conversation] façade (ADR-0026) rather than spelling the bus
// wiring out inline. Feeding frames is all the test does imperatively; the
// conversation segments utterances, transcribes, routes, and replies on the
// bus. The assertions read the recorded event log the same way as before.
func TestSTT_TTRPGIntro_TranscribesBothLanguages(t *testing.T) {
	for _, testCase := range []struct {
		clipName string
		lang     string
		want     string
		response string
	}{
		{
			clipName: "ttrpg-intro-de",
			lang:     "de",
			want: "Hallo zusammen, dann lasst uns doch mal die heutige Session beginnen. " +
				"Okay, Glyphoxa Butler, wiederhol doch einfach einmal bitte was letzte Session so passiert ist, " +
				"was - mach ne kurze Zusammenfassung und - ja wo sind wir am Ende bei raus gekommen?",
			response: "Ja natürlich, letztes mal habt ihr alles umgehauen!",
		},
		{
			clipName: "ttrpg-intro-en",
			lang:     "en",
			want: "Hey everyone, so let's start our session for today. " +
				"Okay, Glyphoxa Butler, can you give us a quick intro what happend last session " +
				"and what did we do, where did we leave the session? What's the current status?",
			response: "Yes of course. Last time you mowed down everything!",
		},
	} {
		t.Run(testCase.clipName, func(t *testing.T) {
			// VAD + audio sample + test harness.
			vadStage, h, frames := voicetest.NewVADRig(t, testCase.clipName)

			// STT transcribes each VAD-segmented utterance.
			recognizer := voicecassette.LoadSTT(t, "stt-"+testCase.clipName)
			sttStage := orchestrator.NewSTT(h.Bus, recognizer)

			// TTS speaks the Butler's reply.
			synthesizer := voicecassette.LoadTTS(t, "tts-"+testCase.clipName)
			ttsStage := orchestrator.NewTTS(h.Bus, synthesizer)
			voice := voicetest.LiveElevenLabsVoice()

			// Address detection: the Butler is the default route; Jamalaka is an
			// active NPC who is never named, so every utterance routes to the Butler.
			detector := orchestrator.NewAddressDetector(
				address.NewWholeWordMatcher(
					voiceevent.AddressTarget{AgentID: "but1", AgentRole: "butler", Name: "Glyphoxa Butler"},
					[]voiceevent.AddressTarget{
						{AgentID: "distraction", AgentRole: "character", Name: "Jamalaka"},
					},
				),
			)

			// Reply strategy: answer the Butler exactly once per turn. The single
			// TTS cassette segment is matched positionally, so a second dispatch
			// would exhaust it — sync.Once pins "one reply".
			var answered sync.Once
			reply := func(_ context.Context, e voiceevent.AddressRouted) []orchestrator.Reply {
				var out []orchestrator.Reply
				if e.Target.AgentRole == "butler" {
					answered.Do(func() {
						out = []orchestrator.Reply{{Sentence: testCase.response, Voice: voice}}
					})
				}
				return out
			}

			// Wire the whole reactive pipeline onto the bus in one call.
			conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
				orchestrator.WithDetector(detector),
				orchestrator.WithReply(reply),
				orchestrator.WithErrorHandler(func(err error) { t.Errorf("reply dispatch: %v", err) }),
			)
			t.Cleanup(conv.Register(t.Context()))

			for i, frame := range frames {
				if err := conv.Feed(frame); err != nil {
					t.Fatalf("frame %d: conv.Feed: %v", i, err)
				}
			}

			// VAD is the most basic thing that should have been noticed.
			voicetest.AssertEventOccurred[voiceevent.VADSpeechStart](t, h)
			voicetest.AssertEventOccurred[voiceevent.VADSpeechEnd](t, h)

			// STT should have been triggered at least once and the transcripts should match.
			voicetest.AssertEventOccurred[voiceevent.STTFinal](t, h)
			want := voicetest.NormalizeTranscript(testCase.want)
			actual := voicetest.NormalizeTranscript(joinTranscripts(h))
			if !voicetest.WordsMatch(want, actual, 0.7) {
				t.Errorf("stt transcript diverged beyond tolerance (<70%% word overlap).\nwant: %q\ngot: %q", want, actual)
			}

			// Every routing decision went to the Butler — no NPC is named in the clip.
			for _, e := range h.Events() {
				if routed, ok := e.(voiceevent.AddressRouted); ok && routed.Target.AgentRole != "butler" {
					t.Errorf("address detection routed to %q, want butler", routed.Target.AgentRole)
				}
			}
			voicetest.AssertEvent(t, h,
				func(e voiceevent.AddressRouted) bool {
					return e.Target.AgentRole == "butler" && e.Target.Name == "Glyphoxa Butler"
				},
				"address.routed exists for Glyphoxa Butler",
			)

			// TTS should have been dispatched with the reply.
			voicetest.AssertEvent(t, h,
				func(e voiceevent.TTSInvoked) bool {
					return e.Sentence == testCase.response
				},
				"tts.invoked should have TTS input",
			)
		})
	}
}

// TestTurnID_PropagatesThroughStages pins the A3 correlation spine: a single
// turn driven through STT → address → reply → TTS produces an STTFinal,
// AddressRouted and TTSInvoked that all carry the SAME non-empty TurnID, so the
// metrics subscriber can join the turn's stage spans.
func TestTurnID_PropagatesThroughStages(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: "roll a d20"}})
	ttsStage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	voice := tts.Voice{ProviderID: "elevenlabs", VoiceID: "george"}

	// Lone-NPC route: every utterance routes to bart (the scoring matcher's
	// single-NPC fallback catches an unnamed utterance).
	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)
	reply := func(_ context.Context, _ voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "Aye.", Voice: voice}}
	}

	// Bind the reply chain (detector → replier → TTS) directly; driving STT via
	// the stage is enough to mint the TurnID and fan the turn out across the bus.
	t.Cleanup(orchestrator.Bind(t.Context(), h.Bus,
		detector,
		orchestrator.NewReplier(ttsStage, reply, func(err error) { t.Errorf("dispatch: %v", err) }),
	))

	if err := sttStage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	var sttID, routedID, ttsID string
	for _, e := range h.Events() {
		switch ev := e.(type) {
		case voiceevent.STTFinal:
			sttID = ev.TurnID
		case voiceevent.AddressRouted:
			routedID = ev.TurnID
		case voiceevent.TTSInvoked:
			ttsID = ev.TurnID
		}
	}
	if sttID == "" {
		t.Fatal("STTFinal carried no TurnID")
	}
	if routedID != sttID {
		t.Errorf("AddressRouted.TurnID = %q, want STTFinal's %q", routedID, sttID)
	}
	if ttsID != sttID {
		t.Errorf("TTSInvoked.TurnID = %q, want STTFinal's %q", ttsID, sttID)
	}
}

// TestTurnID_PropagatesThroughBargeInFloor is the production-path twin of
// TestTurnID_PropagatesThroughStages: with WithBargeIn(0) the turn runs on the
// floor's per-turn context (a goroutine), not synchronously. The TurnID is
// installed before Floor.Take, so it must survive the floor's WithCancel-derived
// context and reach TTSInvoked — the only configuration that actually ships.
func TestTurnID_PropagatesThroughBargeInFloor(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: "hi bart"}})
	ttsStage := orchestrator.NewTTS(h.Bus, closedChanSynth{})
	voice := tts.Voice{ProviderID: "elevenlabs", VoiceID: "george"}

	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)
	reply := func(_ context.Context, _ voiceevent.AddressRouted) []orchestrator.Reply {
		return []orchestrator.Reply{{Sentence: "Aye.", Voice: voice}}
	}

	// VAD stage is unused here (we drive STT directly), but the Conversation
	// requires one; the scripted VAD with no script reports only silence.
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{})
	conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		orchestrator.WithReply(reply),
		orchestrator.WithBargeIn(0), // production wiring: turn runs on the floor goroutine
		orchestrator.WithErrorHandler(func(err error) { t.Errorf("dispatch: %v", err) }),
	)
	t.Cleanup(conv.Register(t.Context()))

	if err := sttStage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	// The turn dispatches on a goroutine; wait for the TTSInvoked it produces.
	var sttID, ttsID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sttID, ttsID = "", ""
		for _, e := range h.Events() {
			switch ev := e.(type) {
			case voiceevent.STTFinal:
				sttID = ev.TurnID
			case voiceevent.TTSInvoked:
				ttsID = ev.TurnID
			}
		}
		if ttsID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sttID == "" {
		t.Fatal("STTFinal carried no TurnID")
	}
	if ttsID == "" {
		t.Fatal("no TTSInvoked observed (turn never dispatched through the floor)")
	}
	if ttsID != sttID {
		t.Errorf("TTSInvoked.TurnID = %q, want STTFinal's %q — the floor's context dropped the turn id", ttsID, sttID)
	}
}

// TestSTT_CarriesSpeechEndAt pins that an utterance segmented by the Segmenter
// carries the turn's VADSpeechEnd time onto the published STTFinal (A3), so the
// headline response-latency span is anchored at the true end-of-speech — and a
// non-empty TurnID is minted. Driven through the scripted-VAD segmenter rig so
// the speech-end transition fires through the real stage.
func TestSTT_CarriesSpeechEndAt(t *testing.T) {
	bus := voiceevent.NewBus()
	var speechEnd time.Time
	voiceevent.On(bus, func(e voiceevent.VADSpeechEnd) { speechEnd = e.At })
	var final voiceevent.STTFinal
	var sawFinal bool
	voiceevent.On(bus, func(e voiceevent.STTFinal) { final, sawFinal = e, true })

	sttStage := orchestrator.NewSTT(bus, stubRecognizer{transcript: stt.Transcript{Text: "hi"}})
	// Script: start, continue (buffered), end → the frame after end flushes.
	vadStage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	seg := orchestrator.NewSegmenter(vadStage, sttStage)
	t.Cleanup(seg.Bind(t.Context(), bus))

	feed(t, seg, 4) // start, continue, end, (flush)

	if speechEnd.IsZero() {
		t.Fatal("no VADSpeechEnd was published")
	}
	if !sawFinal {
		t.Fatal("no STTFinal was published")
	}
	if !final.SpeechEndAt.Equal(speechEnd) {
		t.Errorf("STTFinal.SpeechEndAt = %v, want the VADSpeechEnd time %v", final.SpeechEndAt, speechEnd)
	}
	if final.TurnID == "" {
		t.Error("STTFinal carried no TurnID")
	}
}

// joinTranscripts concatenates the text of every STTFinal observed by h, in
// arrival order, into a single transcript for whole-utterance assertions.
func joinTranscripts(h *voicetest.Harness) string {
	var b strings.Builder
	for _, e := range h.Events() {
		final, ok := e.(voiceevent.STTFinal)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(final.Text)
	}
	return b.String()
}

// TestSTT_EmptyTranscript_StillPublishes pins the contract documented on
// [orchestrator.STT.Transcribe]: when the recognizer authoritatively returns
// an empty transcript (e.g. all silence reached it), the stage still
// publishes [voiceevent.STTFinal] with Text == "". Filtering "heard nothing"
// signals is a downstream decision, not the orchestrator's.
func TestSTT_EmptyTranscript_StillPublishes(t *testing.T) {
	h := voicetest.New(t)
	stage := orchestrator.NewSTT(h.Bus, stubRecognizer{transcript: stt.Transcript{Text: ""}})

	if err := stage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("stage.Transcribe: %v", err)
	}

	voicetest.AssertEvent(t, h,
		func(e voiceevent.STTFinal) bool { return e.Text == "" },
		"stt.final with empty text",
	)
}
