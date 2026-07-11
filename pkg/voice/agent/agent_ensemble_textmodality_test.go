package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// TestReplier_SpeakDraft_VoicelessButler_DeliversTextNoDispatch is the #389
// headline: a voiceless Butler co-addressed in an Ensemble Turn (won the Lead race,
// its draft handed to SpeakDraft) must deliver its draft as channel TEXT and NEVER
// dispatch TTS — a Butler with an empty VoiceID has no Voice to speak with, so an
// empty-VoiceID dispatch must be STRUCTURALLY unreachable. SpeakDraft posts the
// whole draft via the TextSink, dispatches nothing, commits user+assistant
// (ADR-0012), and returns the [orchestrator.ErrTextDelivered] terminal sentinel.
func TestReplier_SpeakDraft_VoicelessButler_DeliversTextNoDispatch(t *testing.T) {
	var posted string
	var dispatched int
	r := textSinkReplier(t, batchEngine{}, true, func(_ context.Context, text string) error {
		posted = text
		return nil
	})

	delivered, err := r.SpeakDraft(t.Context(), "Glyphoxa, roll two d6", "Nine.", func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("SpeakDraft err = %v, want ErrTextDelivered", err)
	}
	if dispatched != 0 {
		t.Errorf("voiceless Butler dispatched %d to TTS, want 0 (empty-VoiceID dispatch must be unreachable)", dispatched)
	}
	if posted != "Nine." {
		t.Errorf("posted = %q, want %q", posted, "Nine.")
	}
	if delivered != "Nine." {
		t.Errorf("delivered = %q, want %q", delivered, "Nine.")
	}
	hist := r.HistorySnapshot()
	if len(hist) != 2 || hist[0].Text != "Glyphoxa, roll two d6" || hist[1].Text != "Nine." {
		t.Fatalf("history = %+v, want user+assistant committed on the text branch", hist)
	}
}

// TestReplier_SpeakDraft_LongAnswer_DeliversText pins the #297 d2 long-answer rule
// on the ensemble path: a VOICED Butler whose winning draft exceeds the size
// threshold posts as text (no TTS dispatch), exactly as the routed streaming path
// does — the ensemble Draft/Speak path consults the SAME modality decision.
func TestReplier_SpeakDraft_LongAnswer_DeliversText(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 200)) // > 400 runes
	var posted string
	var dispatched int
	r := textSinkReplier(t, batchEngine{}, false, func(_ context.Context, text string) error {
		posted = text
		return nil
	})

	delivered, err := r.SpeakDraft(t.Context(), "Glyphoxa, recap last session", long, func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("SpeakDraft err = %v, want ErrTextDelivered (long answer → text)", err)
	}
	if dispatched != 0 {
		t.Errorf("long answer dispatched %d to TTS, want 0", dispatched)
	}
	if posted != long || delivered != long {
		t.Errorf("posted/delivered mismatch, want the whole long answer")
	}
}

// TestReplier_SpeakDraft_ShortVoicedAnswer_Spoken pins that a TextSink does not
// hijack a SHORT VOICED answer: a Butler with a real Voice speaks a short draft
// (sentence-split dispatch), never touching the sink and never returning the
// text-delivered sentinel — modality is decided the same way as the routed path.
func TestReplier_SpeakDraft_ShortVoicedAnswer_Spoken(t *testing.T) {
	sinkCalled := false
	r := textSinkReplier(t, batchEngine{}, false, func(context.Context, string) error {
		sinkCalled = true
		return nil
	})

	var got []string
	delivered, err := r.SpeakDraft(t.Context(), "Glyphoxa, roll two d6", "Two sixes. Total nine.", func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("SpeakDraft errored: %v", err)
	}
	if sinkCalled {
		t.Error("TextSink called for a short voiced answer, want spoken via TTS")
	}
	want := []string{"Two sixes.", "Total nine."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dispatched %q, want %q (sentence-split spoken)", got, want)
	}
	if delivered != "Two sixes. Total nine." {
		t.Errorf("delivered = %q, want the whole spoken draft", delivered)
	}
}

// TestReplier_SpeakDraft_TextSink_PostError_NotCommitted pins ADR-0012
// deliver-then-commit on the ensemble text branch: a failed TextSink post means the
// answer was never delivered, so nothing is committed and the sentinel is NOT
// returned (the coordinator must not record a text_delivered success).
func TestReplier_SpeakDraft_TextSink_PostError_NotCommitted(t *testing.T) {
	postErr := errors.New("channel post failed")
	r := textSinkReplier(t, batchEngine{}, true, func(context.Context, string) error {
		return postErr
	})

	delivered, err := r.SpeakDraft(t.Context(), "Glyphoxa, roll two d6", "Nine.", func(orchestrator.Reply) error {
		t.Fatal("a text-modality answer must not dispatch TTS")
		return nil
	})
	if !errors.Is(err, postErr) {
		t.Fatalf("SpeakDraft err = %v, want the post error", err)
	}
	if errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatal("a failed post must not report ErrTextDelivered")
	}
	if delivered != "" {
		t.Errorf("delivered = %q, want empty on a failed post", delivered)
	}
	if len(r.HistorySnapshot()) != 0 {
		t.Errorf("a failed post committed to history: %+v", r.HistorySnapshot())
	}
}

// TestReplier_SpeakReaction_VoicelessButler_DeliversTextNoDispatch pins the
// structural-unreachability AC on the CROSS-TALK REACTION path (#302/#389): because
// SpeakReaction delegates to SpeakDraft, a voiceless Butler elected as reactor also
// delivers its Reaction as text and dispatches ZERO TTS — a voiceless Butler never
// TTS-dispatches on ANY ensemble path.
func TestReplier_SpeakReaction_VoicelessButler_DeliversTextNoDispatch(t *testing.T) {
	var posted string
	var dispatched int
	r := textSinkReplier(t, batchEngine{}, true, func(_ context.Context, text string) error {
		posted = text
		return nil
	})

	delivered, err := r.SpeakReaction(t.Context(), "Bart, Glyphoxa — thoughts?", "Bart", "We ride at dawn.", "A fine plan.", func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("SpeakReaction err = %v, want ErrTextDelivered", err)
	}
	if dispatched != 0 {
		t.Errorf("voiceless Butler reactor dispatched %d to TTS, want 0", dispatched)
	}
	if posted != "A fine plan." || delivered != "A fine plan." {
		t.Errorf("posted=%q delivered=%q, want the reaction delivered as text", posted, delivered)
	}
}
