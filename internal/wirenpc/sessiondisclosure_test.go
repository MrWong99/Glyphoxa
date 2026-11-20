package wirenpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/tape"
)

// TestTranscriptionDisclosure_LinksPrivacyPolicy is #519 AC2: the notice carries
// the deployment's privacy-policy URL when one is configured, and stays a clean
// (link-free) disclosure when it is not — never a broken link.
func TestTranscriptionDisclosure_LinksPrivacyPolicy(t *testing.T) {
	const url = "https://glyphoxa.example/privacy"

	got := transcriptionDisclosure(url)
	if !strings.Contains(got, url) {
		t.Errorf("disclosure = %q, want the privacy-policy URL %q in it", got, url)
	}
	if !strings.Contains(strings.ToLower(got), "transcribed") {
		t.Errorf("disclosure = %q, want it to say the session is transcribed", got)
	}

	// Whitespace-only config is "not configured".
	if got := transcriptionDisclosure("   "); got != transcriptionDisclosureContent {
		t.Errorf("disclosure with blank URL = %q, want the bare notice", got)
	}
	if strings.Contains(transcriptionDisclosure(""), "http") {
		t.Errorf("disclosure with no URL configured = %q, want no link at all", transcriptionDisclosure(""))
	}
}

// TestSessionDisclosureContent_CombinesWithTape is #519 AC3: a tape-armed
// Campaign gets ONE message carrying both notices (no double-post), while an
// unarmed Campaign gets the transcription notice alone — the tape's consent copy
// must never appear for a session that records nothing.
func TestSessionDisclosureContent_CombinesWithTape(t *testing.T) {
	combined := sessionDisclosureContent("https://glyphoxa.example/privacy", true)
	if !strings.Contains(combined, transcriptionDisclosureContent) {
		t.Error("combined disclosure dropped the transcription notice")
	}
	if !strings.Contains(combined, tapeDisclosureContent) {
		t.Error("combined disclosure dropped the tape consent notice")
	}

	alone := sessionDisclosureContent("", false)
	if strings.Contains(alone, tapeDisclosureContent) {
		t.Errorf("unarmed disclosure = %q, want no Session Highlights consent copy", alone)
	}
	if !strings.Contains(alone, transcriptionDisclosureContent) {
		t.Errorf("unarmed disclosure = %q, want the transcription notice", alone)
	}
}

// disclosureSpy swaps postSessionDisclosure for the test's lifetime, recording
// every post the cycle attempts (and optionally failing them).
type disclosureSpy struct {
	posts   []string
	buttons []bool
	err     error
}

func (s *disclosureSpy) install(t *testing.T) {
	t.Helper()
	orig := postSessionDisclosure
	postSessionDisclosure = func(_ context.Context, _ *bot.Client, _ snowflake.ID, _ uuid.UUID, content string, withButtons bool) error {
		s.posts = append(s.posts, content)
		s.buttons = append(s.buttons, withButtons)
		return s.err
	}
	t.Cleanup(func() { postSessionDisclosure = orig })
}

// TestDiscloseSession_OncePerSessionAcrossCycles is #519 AC1 at the call site:
// five reconnect cycles of one Voice Session post the disclosure exactly once,
// and a tape-armed Campaign gets the consent buttons on that one message.
func TestDiscloseSession_OncePerSessionAcrossCycles(t *testing.T) {
	spy := &disclosureSpy{}
	spy.install(t)

	cfg := Config{CampaignID: uuid.New(), PrivacyPolicyURL: "https://glyphoxa.example/privacy", Tape: tape.New(tape.Window, nil, discard())}
	t.Cleanup(cfg.Tape.Close)

	var disclosed sessionOnce
	for range 5 {
		discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())
	}

	if len(spy.posts) != 1 {
		t.Fatalf("posted %d disclosures across 5 reconnect cycles, want 1", len(spy.posts))
	}
	if !strings.Contains(spy.posts[0], "https://glyphoxa.example/privacy") {
		t.Errorf("posted disclosure = %q, want the privacy-policy link", spy.posts[0])
	}
	if !strings.Contains(spy.posts[0], tapeDisclosureContent) {
		t.Error("tape-armed session lost its consent disclosure — it must ride the same message, not a second post")
	}
	if !spy.buttons[0] {
		t.Error("tape-armed disclosure posted without the Consent/Revoke buttons")
	}
}

// TestDiscloseSession_UnarmedPostsPlainNoticeAndFailureIsBestEffort: an unarmed
// Campaign still gets the transcription notice — with no consent buttons — and a
// Discord rejection is swallowed (logged) rather than failing the cycle.
func TestDiscloseSession_UnarmedPostsPlainNoticeAndFailureIsBestEffort(t *testing.T) {
	spy := &disclosureSpy{err: errors.New("discord said no")}
	spy.install(t)

	cfg := Config{CampaignID: uuid.New()}
	var disclosed sessionOnce
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())

	if len(spy.posts) != 1 {
		t.Fatalf("posted %d disclosures, want 1", len(spy.posts))
	}
	if spy.buttons[0] {
		t.Error("unarmed session got tape consent buttons")
	}
	if strings.Contains(spy.posts[0], tapeDisclosureContent) {
		t.Errorf("unarmed disclosure = %q, want no Session Highlights copy", spy.posts[0])
	}
}

// TestDiscloseSession_FailedPostRetriesNextCycle: the disclosure is the only
// consent surface players see, so a post Discord rejected (a 403 in a locked-down
// channel, a REST timeout) must be tried again on the next reconnect cycle — and
// once one lands, never again.
func TestDiscloseSession_FailedPostRetriesNextCycle(t *testing.T) {
	spy := &disclosureSpy{err: errors.New("discord said no")}
	spy.install(t)

	cfg := Config{CampaignID: uuid.New()}
	var disclosed sessionOnce
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())
	spy.err = nil
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())

	if len(spy.posts) != 2 {
		t.Fatalf("posted %d disclosures, want 2 (the failed one retried once, then done)", len(spy.posts))
	}
}

// TestDiscloseSession_BoundsThePost: the REST call runs under a deadline, so a
// Discord that never answers cannot pin the cycle that posts it.
func TestDiscloseSession_BoundsThePost(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	orig := postSessionDisclosure
	postSessionDisclosure = func(ctx context.Context, _ *bot.Client, _ snowflake.ID, _ uuid.UUID, _ string, _ bool) error {
		deadline, hasDeadline = ctx.Deadline()
		return nil
	}
	t.Cleanup(func() { postSessionDisclosure = orig })

	var disclosed sessionOnce
	discloseSession(context.Background(), nil, snowflake.ID(7), Config{CampaignID: uuid.New()}, &disclosed, discard())
	if !hasDeadline {
		t.Fatal("disclosure post ran without a deadline")
	}
	if time.Until(deadline) > disclosurePostTimeout {
		t.Errorf("deadline %s away, want at most %s", time.Until(deadline), disclosurePostTimeout)
	}
}

// TestDisclosureMessage_SuppressesMentions (S8): the notice carries a configured
// URL and, for the Butler poster, LLM text — no message the Bot posts may ping.
func TestDisclosureMessage_SuppressesMentions(t *testing.T) {
	msg := disclosureMessage(uuid.New(), "@everyone hi", true)
	assertNoMentions(t, msg.AllowedMentions)
}

// assertNoMentions fails unless am suppresses every mention type: present, with
// an empty (non-nil, so it serializes as []) parse list and no allowed ids.
func assertNoMentions(t *testing.T, am *discord.AllowedMentions) {
	t.Helper()
	if am == nil {
		t.Fatal("AllowedMentions = nil; Discord then parses @everyone/@here/role/user mentions in the content")
	}
	if am.Parse == nil || len(am.Parse) != 0 || len(am.Users) != 0 || len(am.Roles) != 0 || am.RepliedUser {
		t.Errorf("AllowedMentions = %+v, want an empty parse list and nothing allowed", am)
	}
}

// TestDisclosureFailureRemedy: a permission rejection names the fix an operator
// can act on; other failures do not invent one.
func TestDisclosureFailureRemedy(t *testing.T) {
	forbidden := &rest.Error{Code: rest.JSONErrorCodeLackPermissionsToPerformAction, Response: &http.Response{StatusCode: http.StatusForbidden}}
	if got := disclosureFailureRemedy(fmt.Errorf("wrapped: %w", forbidden)); !strings.Contains(got, "Send Messages") {
		t.Errorf("remedy for a 403 = %q, want it to name the Send Messages permission", got)
	}
	if got := disclosureFailureRemedy(errors.New("timeout")); strings.Contains(got, "Send Messages") {
		t.Errorf("remedy for a timeout = %q, want no permission advice", got)
	}
}

// TestSessionOnce_FiresUntilSuccess is #519 AC1: the guard keeps the disclosure
// to one LANDED post per Voice Session however many reconnect cycles the session
// survives; a failed attempt does not arm it.
func TestSessionOnce_FiresUntilSuccess(t *testing.T) {
	var once sessionOnce
	calls := 0
	for range 5 { // five reconnect cycles
		once.do(func() bool { calls++; return true })
	}
	if calls != 1 {
		t.Errorf("disclosure posted %d times across 5 cycles, want exactly 1", calls)
	}

	var failing sessionOnce
	tries := 0
	for range 3 {
		failing.do(func() bool { tries++; return false })
	}
	if tries != 3 {
		t.Errorf("a failing side effect ran %d times across 3 cycles, want 3 (retried each cycle)", tries)
	}

	// A nil guard (a cycle built outside Run) posts nothing rather than panicking.
	var nilOnce *sessionOnce
	nilOnce.do(func() bool { t.Error("nil sessionOnce fired"); return true })
}
