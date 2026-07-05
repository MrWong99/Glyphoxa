package presence

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDispatchDeferExtendsDeadline pins issue #1a: a Defer stops the
// first-response watchdog, so a slow deferred handler (a #120 transcript search)
// is NOT killed with DeadlineExceeded when it runs past the ~3s window.
func TestDispatchDeferExtendsDeadline(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.responseTimeout = 20 * time.Millisecond

	var ctxErrAfterWork error
	reg.Register(Command{Path: "slow", Handle: func(ctx context.Context, ic *Interaction) error {
		if err := ic.Defer(false); err != nil {
			return err
		}
		time.Sleep(60 * time.Millisecond) // well past the 20ms watchdog
		ctxErrAfterWork = ctx.Err()
		return ic.Followup("done", false)
	}})

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "slow", &Interaction{guildID: testGuild, resp: resp})

	if ctxErrAfterWork != nil {
		t.Errorf("deferred handler ctx = %v after work; a Defer must stop the first-response watchdog", ctxErrAfterWork)
	}
	if resp.deferred == nil {
		t.Error("no Defer recorded")
	}
	if len(resp.followups) != 1 || resp.followups[0].content != "done" {
		t.Errorf("followups = %+v, want one 'done'", resp.followups)
	}
	if len(resp.replies) != 0 {
		t.Errorf("a deferred handler must not CreateMessage; replies = %+v", resp.replies)
	}
}

// TestDispatchDeferredHandlerErrorRepliesViaFollowup pins issue #1b: after a
// Defer the interaction is already acknowledged, so the Registry's generic error
// reply must go through Followup — a fresh CreateMessage would be a Discord 40060
// and the user would be stuck on "thinking…".
func TestDispatchDeferredHandlerErrorRepliesViaFollowup(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "boom", Handle: func(_ context.Context, ic *Interaction) error {
		if err := ic.Defer(true); err != nil {
			return err
		}
		return context.DeadlineExceeded // an unexpected failure after the ACK
	}})

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "boom", &Interaction{guildID: testGuild, resp: resp})

	if len(resp.replies) != 0 {
		t.Errorf("error reply after Defer used CreateMessage (Discord 40060); replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("error reply after Defer = %+v, want one ephemeral Followup", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "went wrong") {
		t.Errorf("followup = %q, want the generic failure message", resp.followups[0].content)
	}
}

// TestDeferredHandlerDomainErrorRepliesViaFollowup covers the handler-owned
// reply path (the convention #108/#120 use): a deferred handler that hits a
// domain error and calls ReplyEphemeral routes it to Followup, not CreateMessage.
func TestDeferredHandlerDomainErrorRepliesViaFollowup(t *testing.T) {
	reg := testRegistry(testGuild, "")
	reg.Register(Command{Path: "search", Handle: func(_ context.Context, ic *Interaction) error {
		_ = ic.Defer(true)
		return ic.ReplyEphemeral("no results")
	}})

	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "search", &Interaction{guildID: testGuild, resp: resp})

	if len(resp.replies) != 0 {
		t.Errorf("domain reply after Defer used CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || resp.followups[0].content != "no results" || !resp.followups[0].ephemeral {
		t.Errorf("followups = %+v, want one ephemeral 'no results'", resp.followups)
	}
}
