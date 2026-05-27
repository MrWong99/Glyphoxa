package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
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

// TestSTT_TTRPGIntro_TranscribesBothLanguages tests that the STT stage can transcribe the TTRPG intro clip in both languages.
// It includes the full audio pipeline with audio -> VAD -> STT -> AddressDetection -> TTS
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
			// VAD and loading of audio sample + test harness
			vadStage, h, frames := voicetest.NewVADRig(t, testCase.clipName)

			// Address detection
			detector := orchestrator.NewAddressDetector(h.Bus, voiceevent.AddressTarget{
				AgentID:   "but1",
				AgentRole: "butler",
				Name:      "Glyphoxa Butler",
			}, []voiceevent.AddressTarget{
				{
					AgentID:   "distraction",
					AgentRole: "character",
					Name:      "Jamalaka",
				},
			})
			t.Cleanup(detector.Close)

			// STT to transcribe audio chunks
			recognizer := voicecassette.LoadSTT(t, "stt-"+testCase.clipName)
			sttStage := orchestrator.NewSTT(h.Bus, recognizer)

			// TTS to generate response when butler is addressed
			synthesizer := voicecassette.LoadTTS(t, "tts-"+testCase.clipName)
			ttsStage := orchestrator.NewTTS(h.Bus, synthesizer)
			voice := voicetest.LiveElevenLabsVoice()
			dispatchOnce := sync.Once{}

			// Full voice pipeline via bus subscription.
			// 1. When VAD detects voice it collects the played frames.
			// 2. Once VAD turns off again all collected samples are processed in STT
			// 3. All STTFinal events are collected to a full transcript
			// 4. If the address detection picks up a message targeted at the butler a TTS response is generated
			listening := false
			allTranscripts := ""
			t.Cleanup(h.Bus.Subscribe(func(e voiceevent.Event) {
				switch event := e.(type) {
				case voiceevent.VADSpeechStart:
					listening = true
				case voiceevent.VADSpeechEnd:
					listening = false
				case voiceevent.STTFinal:
					if allTranscripts == "" {
						allTranscripts = event.Text
					} else {
						allTranscripts += " " + event.Text
					}
				case voiceevent.AddressRouted:
					if event.Target.AgentRole != "butler" {
						t.Error("address detection tried to route to npc")
						return
					}
					dispatchOnce.Do(func() {
						if err := ttsStage.Dispatch(t.Context(), testCase.response, voice); err != nil {
							t.Fatalf("tts failed to generate response: %v", err)
						}
					})
				}
			}))

			framesSinceLastVad := make([]audio.Frame, 0, len(frames))
			for _, frame := range frames {
				if err := vadStage.Process(frame); err != nil {
					t.Fatalf("unexpected error during vad processing: %v", err)
				}
				if listening {
					framesSinceLastVad = append(framesSinceLastVad, frame)
				} else if len(framesSinceLastVad) > 0 {
					// Listening must have switched off since we have leftover frames with listening == false.
					// This signals a finished utterance to give to STT.
					if err := sttStage.Transcribe(t.Context(), framesSinceLastVad); err != nil {
						t.Fatalf("unexpected error during stt processing: %v", err)
					}
					framesSinceLastVad = make([]audio.Frame, 0, len(frames))
				}
			}
			if listening {
				t.Error("VAD should have emitted a VADSpeechEnd last")
			}

			// VAD is the most basic thing that should have been noticed
			voicetest.AssertEventOccurred[voiceevent.VADSpeechStart](t, h)
			voicetest.AssertEventOccurred[voiceevent.VADSpeechEnd](t, h)

			// STT should have been triggered at least once and the transcripts should match
			voicetest.AssertEventOccurred[voiceevent.STTFinal](t, h)
			want := voicetest.NormalizeTranscript(testCase.want)
			actual := voicetest.NormalizeTranscript(allTranscripts)
			if !voicetest.WordsMatch(want, actual, 0.7) {
				t.Errorf("stt transcript diverged beyond tolerance (<70%% word overlap).\nwant: %q\ngot: %q", want, actual)
			}

			// Address detection should have found the butler
			voicetest.AssertEvent(t, h,
				func(e voiceevent.AddressRouted) bool {
					return e.Target.AgentRole == "butler" && e.Target.Name == "Glyphoxa Butler"
				},
				"address.routed exists for Glyphoxa Butler",
			)

			// TTS should have been dispatched
			voicetest.AssertEvent(t, h,
				func(e voiceevent.TTSInvoked) bool {
					return e.Sentence == testCase.response
				},
				"tts.invoked should have TTS input",
			)
		})
	}
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
