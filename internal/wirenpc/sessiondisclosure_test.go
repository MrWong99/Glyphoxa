package wirenpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/bot"
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
// Discord rejection is swallowed (logged) rather than failing the cycle, and is
// not retried on the next one.
func TestDiscloseSession_UnarmedPostsPlainNoticeAndFailureIsBestEffort(t *testing.T) {
	spy := &disclosureSpy{err: errors.New("discord said no")}
	spy.install(t)

	cfg := Config{CampaignID: uuid.New()}
	var disclosed sessionOnce
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())
	discloseSession(context.Background(), nil, snowflake.ID(7), cfg, &disclosed, discard())

	if len(spy.posts) != 1 {
		t.Fatalf("posted %d disclosures, want 1 (a failed post is not retried into a spam loop)", len(spy.posts))
	}
	if spy.buttons[0] {
		t.Error("unarmed session got tape consent buttons")
	}
	if strings.Contains(spy.posts[0], tapeDisclosureContent) {
		t.Errorf("unarmed disclosure = %q, want no Session Highlights copy", spy.posts[0])
	}
}

// TestSessionOnce_FiresExactlyOnce is #519 AC1: the guard that keeps the
// disclosure to one post per Voice Session however many reconnect cycles the
// session survives — including when the post itself failed (arming happens
// before the call, so a rejected post is not retried into a spam loop).
func TestSessionOnce_FiresExactlyOnce(t *testing.T) {
	var once sessionOnce
	calls := 0
	for range 5 { // five reconnect cycles
		once.do(func() { calls++ })
	}
	if calls != 1 {
		t.Errorf("disclosure posted %d times across 5 cycles, want exactly 1", calls)
	}

	// A nil guard (a cycle built outside Run) posts nothing rather than panicking.
	var nilOnce *sessionOnce
	nilOnce.do(func() { t.Error("nil sessionOnce fired") })
}
