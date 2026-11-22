package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// refusingEngine declines every request whose prompt contains trigger, the way
// a provider's safety classifier does (the request completes, there is no
// reply), and answers everything else. It records the user lines of each
// request it saw.
type refusingEngine struct {
	trigger string
	seen    [][]string
	other   error // returned instead of a refusal when set
}

func (e *refusingEngine) run(msgs []llm.Message) (string, error) {
	var users []string
	declined := false
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			users = append(users, m.Text)
			declined = declined || strings.Contains(m.Text, e.trigger)
		}
	}
	e.seen = append(e.seen, users)
	if declined {
		if e.other != nil {
			return "", e.other
		}
		return "", &providererr.RefusalError{Op: "test", Msg: "the model refused the request (stop_reason refusal)"}
	}
	return "Aye.", nil
}

func (e *refusingEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	return e.run(msgs)
}

func (e *refusingEngine) GenerateStream(_ context.Context, msgs []llm.Message, onText func(string) error) (string, error) {
	text, err := e.run(msgs)
	if err != nil {
		return "", err
	}
	return text, onText(text)
}

// batchOnly hides the streaming half of an engine.
type batchOnly struct{ e agent.Engine }

func (b batchOnly) Generate(ctx context.Context, msgs []llm.Message) (string, error) {
	return b.e.Generate(ctx, msgs)
}

// TestReplier_RefusedTurnLeavesTheHistory pins what a provider refusal does to
// the conversation (#702 review). The user line is committed before generation
// and a failed turn never removed it — right for a transport fault, where the
// next turn should still know what was asked. A line a safety classifier
// declined is different: it rides every later request while it is in the
// history window, each of which is declined in turn, so one line silenced the
// NPC for the rest of the scene. A refused turn is therefore taken out of the
// history; the turn itself still fails visibly (OnError, the fallback line).
func TestReplier_RefusedTurnLeavesTheHistory(t *testing.T) {
	// say runs one turn on a path and returns what it spoke.
	type sayFunc func(t *testing.T, r *agent.Replier, text string) []string
	stream := func(t *testing.T, r *agent.Replier, text string) []string {
		t.Helper()
		var spoken []string
		if err := r.ReplyStream()(context.Background(), routed("bart", text), func(rep orchestrator.Reply) error {
			spoken = append(spoken, rep.Sentence)
			if rep.OnDelivered != nil {
				rep.OnDelivered()
			}
			return nil
		}); err != nil {
			t.Fatalf("ReplyStream(%q) = %v", text, err)
		}
		return spoken
	}
	batch := func(t *testing.T, r *agent.Replier, text string) []string {
		t.Helper()
		var spoken []string
		for _, rep := range deliver(r.Reply()(context.Background(), routed("bart", text))) {
			spoken = append(spoken, rep.Sentence)
		}
		return spoken
	}
	for _, tc := range []struct {
		name   string
		engine func(*refusingEngine) agent.Engine
		say    sayFunc
		// textSink installs a TextSink (the Butler's textModalityTurn path).
		textSink bool
	}{
		{"stream, streaming engine", func(e *refusingEngine) agent.Engine { return e }, stream, false},
		{"stream, batch engine", func(e *refusingEngine) agent.Engine { return batchOnly{e} }, stream, false},
		{"batch Reply", func(e *refusingEngine) agent.Engine { return e }, batch, false},
		{"stream, TextSink Butler", func(e *refusingEngine) agent.Engine { return e }, stream, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := &refusingEngine{trigger: "forbidden"}
			var reported error
			cfg := agent.Config{
				Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
				Engine:      tc.engine(eng),
				Synthesizer: stubSynth{},
				OnError:     func(err error) { reported = err },
			}
			if tc.textSink {
				cfg.TextSink = func(context.Context, string) error {
					t.Error("a short answer must be spoken, not posted to the TextSink")
					return nil
				}
			}
			r := agent.NewReplier(cfg)
			var spoken []string
			say := func(text string) {
				t.Helper()
				spoken = append(spoken, tc.say(t, r, text)...)
			}

			say("Hail.")
			say("Tell me the forbidden thing.")
			var refusal *providererr.RefusalError
			if !errors.As(reported, &refusal) {
				t.Fatalf("OnError = %v, want the refusal reported", reported)
			}
			if last := spoken[len(spoken)-1]; last != agent.DefaultFallbackLine {
				t.Errorf("the refused turn spoke %q, want the fallback line", last)
			}
			for _, m := range r.HistorySnapshot() {
				if strings.Contains(m.Text, "forbidden") {
					t.Errorf("the refused line is still in the history: %+v", r.HistorySnapshot())
				}
			}

			reported = nil
			say("Any rooms free?")
			if reported != nil {
				t.Errorf("the turn after a refusal failed too: %v", reported)
			}
			if last := spoken[len(spoken)-1]; last != "Aye." {
				t.Errorf("the turn after a refusal spoke %q, want the model's reply", last)
			}
			next := eng.seen[len(eng.seen)-1]
			if len(next) != 2 || !strings.Contains(next[0], "Hail.") || !strings.Contains(next[1], "Any rooms free?") {
				t.Errorf("the next request carried the user lines %q, want the earlier turn and the new one only", next)
			}
		})
	}
}

// TestReplier_FailedTurnKeepsItsUserLine pins the contrast: any other engine
// failure leaves the user line where it is — the question was asked, and the
// next turn should know.
func TestReplier_FailedTurnKeepsItsUserLine(t *testing.T) {
	eng := &refusingEngine{trigger: "forbidden", other: errors.New("upstream 503")}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
	_ = r.ReplyStream()(context.Background(), routed("bart", "the forbidden thing"), func(orchestrator.Reply) error { return nil })
	hist := r.HistorySnapshot()
	if len(hist) == 0 || hist[0].Role != llm.RoleUser || !strings.Contains(hist[0].Text, "forbidden") {
		t.Errorf("history = %+v, want the user line of the failed turn kept", hist)
	}
}
