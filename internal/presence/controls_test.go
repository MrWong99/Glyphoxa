package presence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// testVoice is a voice blob for test NPCs that can be heard.
var testVoice = []byte(`{"VoiceID":"v-1"}`)

// erroringMuter is a SessionMuter whose live session exists but whose writes
// fail with err.
type erroringMuter struct {
	fakeMuter
	err error
}

func (e *erroringMuter) SetAgentMute(context.Context, uuid.UUID, string, bool) ([]string, error) {
	return nil, e.err
}

func (e *erroringMuter) SetAllMute(context.Context, uuid.UUID, bool) ([]string, error) {
	return nil, e.err
}

// TestLiveControls_DistinguishNoSessionFromFailure (S4): a session that ended
// mid-command still reads as the no-session guard, but any other failure is
// logged and answered as a failure — not "No voice session is running" while
// one plainly is.
func TestLiveControls_DistinguishNoSessionFromFailure(t *testing.T) {
	bart := storage.Agent{ID: uuid.New(), Name: "Bart", Voice: testVoice}
	agents := &fakeLister{agents: []storage.Agent{bart}}
	boom := errors.New("db: connection reset")

	cases := []struct {
		name      string
		err       error
		wantReply string
		wantLog   bool
	}{
		{"session ended", fmt.Errorf("wrapped: %w", session.ErrNoActiveSession), noSessionReply, false},
		{"unexpected", boom, controlFailedReply, true},
	}
	for _, tc := range cases {
		run := map[string]func(ic *Interaction) error{
			"mute": func(ic *Interaction) error {
				return MuteCommand(&erroringMuter{fakeMuter{active: true}, tc.err}, agents, nil).Handle(context.Background(), ic)
			},
			"muteall": func(ic *Interaction) error {
				return MuteAllCommand(&erroringMuter{fakeMuter{active: true}, tc.err}, agents, nil).Handle(context.Background(), ic)
			},
			"say": func(ic *Interaction) error {
				return SayCommand(&fakeSayer{active: true, sayErr: tc.err}, agents, nil).Handle(context.Background(), ic)
			},
			"direct": func(ic *Interaction) error {
				return DirectCommand(&fakeDirector{active: true, directErr: tc.err}, agents, nil).Handle(context.Background(), ic)
			},
		}
		for cmd, handle := range run {
			t.Run(tc.name+"/"+cmd, func(t *testing.T) {
				var logs bytes.Buffer
				resp := &fakeResponder{}
				ic := &Interaction{
					guildID: testGuild, userID: operatorID, resp: resp,
					opts: fakeOpts{s: map[string]string{"npc": "Bart", "as": "Bart", "text": "hi", "note": "lie"}},
					log:  slog.New(slog.NewTextHandler(&logs, nil)),
				}
				if err := handle(ic); err != nil {
					t.Fatalf("Handle: %v", err)
				}
				if len(resp.replies) != 1 || resp.replies[0].content != tc.wantReply {
					t.Fatalf("reply = %+v, want %q", resp.replies, tc.wantReply)
				}
				if logged := strings.Contains(logs.String(), boom.Error()); logged != tc.wantLog {
					t.Errorf("logged the cause = %v, want %v (logs: %s)", logged, tc.wantLog, logs.String())
				}
			})
		}
	}
}

// TestSayCommand_VoicelessNPCRefused: an NPC with no voice is never heard, so
// /say must not claim "Speaking as" it.
func TestSayCommand_VoicelessNPCRefused(t *testing.T) {
	mute := storage.Agent{ID: uuid.New(), Name: "Mute Mo"}
	sayer := &fakeSayer{active: true}
	resp := &fakeResponder{}
	if err := SayCommand(sayer, &fakeLister{agents: []storage.Agent{mute}}, nil).Handle(context.Background(), sayIC(resp, "hello", "Mute Mo")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sayer.calls) != 0 {
		t.Errorf("SayAs called for a voiceless NPC: %v", sayer.calls)
	}
	if len(resp.replies) != 1 || strings.Contains(resp.replies[0].content, "Speaking as") || !strings.Contains(resp.replies[0].content, "no voice") {
		t.Errorf("reply = %+v, want the no-voice explanation", resp.replies)
	}
}

// TestMuteReplies_PointToWebUnmute: Discord has no unmute command, so both
// mute confirmations say where un-muting lives.
func TestMuteReplies_PointToWebUnmute(t *testing.T) {
	for _, got := range []string{muteConfirmation("Bart"), muteAllConfirmation(3)} {
		if !strings.Contains(got, "Unmute") || !strings.Contains(got, "Session screen") {
			t.Errorf("%q does not say unmuting is on the web Session screen", got)
		}
	}
	if desc := MuteCommand(&fakeMuter{}, &fakeLister{}, nil).Options[0].(discord.ApplicationCommandOptionString).Description; strings.Contains(desc, "Butler") {
		t.Errorf("mute option description %q promises the Butler, which is never a mute target", desc)
	}
}

// TestReplies_SuppressMentions (S8): every interaction reply and placeholder
// edit carries an empty allowed-mentions list — a public recap quotes LLM and
// transcript text, and "@everyone" in it must not ping the server.
func TestReplies_SuppressMentions(t *testing.T) {
	for _, msg := range []discord.MessageCreate{ephemeralMessage("@everyone", true), ephemeralMessage("@everyone", false)} {
		if msg.AllowedMentions == nil || msg.AllowedMentions.Parse == nil || len(msg.AllowedMentions.Parse) != 0 {
			t.Errorf("reply AllowedMentions = %+v, want an empty parse list", msg.AllowedMentions)
		}
	}
	if edit := placeholderEdit("@here"); edit.AllowedMentions == nil || edit.AllowedMentions.Parse == nil || len(edit.AllowedMentions.Parse) != 0 {
		t.Errorf("placeholder edit AllowedMentions = %+v, want an empty parse list", edit.AllowedMentions)
	}
}

// slowTenants is a TenantResolver that takes delay to answer.
type slowTenants struct {
	fakeTenants
	delay time.Duration
}

func (s slowTenants) TenantForGuild(ctx context.Context, guildID string) (uuid.UUID, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return uuid.Nil, ctx.Err()
	}
	return s.fakeTenants.TenantForGuild(ctx, guildID)
}

// TestDispatch_GateAndHandlerShareOneDeadline (S6): authorization and the
// handler draw on ONE first-response budget. They used to get 2s and 2.5s back
// to back, so a slow Gate pushed the handler's deadline past Discord's 3s.
func TestDispatch_GateAndHandlerShareOneDeadline(t *testing.T) {
	const budget = 300 * time.Millisecond
	const gateDelay = 200 * time.Millisecond
	reg := NewRegistry(NewGate(gmIn(tenantA, operatorID), slowTenants{fakeTenants{testGuild: tenantA}, gateDelay}), nil)
	reg.responseTimeout = budget

	var cancelledAfter time.Duration
	start := time.Now()
	reg.Register(Command{Path: "slow", Handle: func(ctx context.Context, ic *Interaction) error {
		<-ctx.Done()
		cancelledAfter = time.Since(start)
		return nil
	}})
	reg.dispatch(context.Background(), "slow", &Interaction{guildID: testGuild, userID: operatorID, resp: &fakeResponder{}})

	if cancelledAfter < gateDelay {
		t.Fatalf("handler cancelled after %s, before the gate even answered", cancelledAfter)
	}
	// Sequential budgets would cancel at gateDelay+budget = 500ms.
	if cancelledAfter > budget+(gateDelay/2) {
		t.Errorf("handler cancelled %s after dispatch began, want about %s: the gate's time must come out of the same budget", cancelledAfter, budget)
	}
}
