package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeRecapEngine records the sessions it was asked to recap and returns a
// canned Result/err, so the handler's session pick + delivery can be asserted
// without a live LLM. With block set it hangs until the ctx is cancelled and
// returns ctx.Err() — the recapOpTimeout stand-in for a stuck LLM.
type fakeRecapEngine struct {
	result recap.Result
	err    error
	block  bool
	calls  int
	gotIDs []uuid.UUID
}

func (f *fakeRecapEngine) Recap(ctx context.Context, ids []uuid.UUID) (recap.Result, error) {
	f.calls++
	f.gotIDs = ids
	if f.block {
		<-ctx.Done()
		return recap.Result{}, ctx.Err()
	}
	return f.result, f.err
}

// fakeButlerVoicer records a voiced-recap request so the voiced happy path can be
// asserted without a live voice loop.
type fakeButlerVoicer struct {
	spoken string
	calls  int
	err    error
}

func (f *fakeButlerVoicer) SpeakAsButler(_ context.Context, _ uuid.UUID, text string) error {
	f.calls++
	f.spoken = text
	return f.err
}

// fakeRecapStore reuses the shared slash Active-Campaign resolver (fakeSessionStore)
// and adds the Voice Session read surface the recap picker needs.
type fakeRecapStore struct {
	*fakeSessionStore
	sessions  []storage.VoiceSession // ListVoiceSessions result (newest-first)
	listErr   error
	byID      map[uuid.UUID]storage.VoiceSession
	getErr    error
	listCalls int
	gotLimit  int
}

func (f *fakeRecapStore) ListVoiceSessions(_ context.Context, _ uuid.UUID, limit int) ([]storage.VoiceSession, error) {
	f.listCalls++
	f.gotLimit = limit
	return f.sessions, f.listErr
}

func (f *fakeRecapStore) GetVoiceSession(_ context.Context, id uuid.UUID) (storage.VoiceSession, error) {
	if f.getErr != nil {
		return storage.VoiceSession{}, f.getErr
	}
	vs, ok := f.byID[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	return vs, nil
}

// recapStore builds a fakeRecapStore whose Active Campaign resolves via the
// operator's durable /glyphoxa use selection, seeded with the given sessions.
func recapStore(selected storage.Campaign, sessions ...storage.VoiceSession) *fakeRecapStore {
	byID := map[uuid.UUID]storage.VoiceSession{}
	for _, vs := range sessions {
		byID[vs.ID] = vs
	}
	return &fakeRecapStore{
		fakeSessionStore: &fakeSessionStore{forUser: &selected},
		sessions:         sessions,
		byID:             byID,
	}
}

func endedSession(campaignID uuid.UUID, start time.Time, lines int) storage.VoiceSession {
	end := start.Add(time.Hour)
	return storage.VoiceSession{
		ID: uuid.New(), CampaignID: campaignID, StartedAt: start, EndedAt: &end,
		Status: storage.VoiceSessionEnded, LineCount: lines,
	}
}

func runningSession(campaignID uuid.UUID, start time.Time) storage.VoiceSession {
	return storage.VoiceSession{
		ID: uuid.New(), CampaignID: campaignID, StartedAt: start,
		Status: storage.VoiceSessionRunning,
	}
}

// dispatchRecap registers /glyphoxa recap on a GM-configured registry and
// dispatches it as the allowlisted operator with the given options.
func dispatchRecap(t *testing.T, store RecapStore, voice VoiceControl, eng RecapEngine, butler ButlerVoicer, opts map[string]string) *fakeResponder {
	t.Helper()
	reg := testRegistry(testGuild, operatorID)
	reg.Register(RecapCommand(store, voice, eng, butler))
	return dispatchAs(reg, "glyphoxa recap", operatorID, opts)
}

// TestRecapCommandShape pins the command surface: grouped /glyphoxa recap, GM-only
// (ADR-0010/0041), with an optional autocompleting `session` option and an optional
// `delivery` option carrying the three decided choices (#271 decision 6).
func TestRecapCommandShape(t *testing.T) {
	cmd := RecapCommand(recapStore(campaign("C")), &fakeVoice{}, &fakeRecapEngine{}, nil)
	if cmd.Path != "glyphoxa recap" {
		t.Errorf("Path = %q, want \"glyphoxa recap\"", cmd.Path)
	}
	if !cmd.GMOnly {
		t.Error("GMOnly = false, want true (recap is GM-only, ADR-0010)")
	}
	if len(cmd.Options) != 2 {
		t.Fatalf("Options = %d, want 2 (session, delivery)", len(cmd.Options))
	}
	sess, ok := cmd.Options[0].(discord.ApplicationCommandOptionString)
	if !ok || sess.Name != "session" || sess.Required || !sess.Autocomplete {
		t.Errorf("option 0 = %+v, want an optional autocompleting string 'session'", cmd.Options[0])
	}
	del, ok := cmd.Options[1].(discord.ApplicationCommandOptionString)
	if !ok || del.Name != "delivery" || del.Required {
		t.Fatalf("option 1 = %+v, want an optional string 'delivery'", cmd.Options[1])
	}
	wantChoices := map[string]bool{deliveryVoiced: false, deliveryPublic: false, deliveryEphemeral: false}
	for _, ch := range del.Choices {
		wantChoices[ch.Value] = true
	}
	for v, seen := range wantChoices {
		if !seen {
			t.Errorf("delivery choice %q missing; got %+v", v, del.Choices)
		}
	}
	if cmd.Autocomplete == nil {
		t.Error("Autocomplete handler is nil, want a session autocomplete")
	}
}

// TestRecapHappyDefault is test-seq (2): no options → Defer(true), the strict
// shared resolver, the LATEST ENDED session picked even while a running session
// exists (NOT the running one), the engine called with exactly that id, and one
// ephemeral Followup carrying the recap prose.
func TestRecapHappyDefault(t *testing.T) {
	c := campaign("Lost Mine")
	old := endedSession(c.ID, time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC), 40)
	latestEnded := endedSession(c.ID, time.Date(2026, 6, 26, 18, 0, 0, 0, time.UTC), 88)
	live := runningSession(c.ID, time.Date(2026, 6, 27, 18, 0, 0, 0, time.UTC))
	// Newest-first, running row on top — the picker must skip it for the ended one.
	store := recapStore(c, live, latestEnded, old)
	eng := &fakeRecapEngine{result: recap.Result{Text: "The party slew the dragon."}}

	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if resp.deferred == nil || !*resp.deferred {
		t.Fatalf("recap must Defer ephemerally by default; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 0 {
		t.Errorf("a deferred handler must not CreateMessage; replies = %+v", resp.replies)
	}
	if eng.calls != 1 || len(eng.gotIDs) != 1 || eng.gotIDs[0] != latestEnded.ID {
		t.Fatalf("engine call = (calls %d, ids %v), want (1, [%s] latest ended)", eng.calls, eng.gotIDs, latestEnded.ID)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(resp.followups[0].content, "slew the dragon") {
		t.Errorf("followup = %q, want the recap prose", resp.followups[0].content)
	}
}

// TestRecapNoActiveCampaign is test-seq (3): no live session and no durable
// selection → the strict resolver's ErrNoActiveCampaign, answered with the SAME
// wording the sibling /glyphoxa commands use, and the engine is never called.
func TestRecapNoActiveCampaign(t *testing.T) {
	store := &fakeRecapStore{fakeSessionStore: &fakeSessionStore{}} // forUser nil -> ErrNotFound
	eng := &fakeRecapEngine{}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if resp.followups[0].content != "No Active Campaign yet — run /glyphoxa use campaign:<name> first." {
		t.Errorf("no-campaign reply = %q, want the exact sibling wording", resp.followups[0].content)
	}
	if eng.calls != 0 {
		t.Errorf("engine ran %d times with no Active Campaign, want 0", eng.calls)
	}
}

// TestRecapNoRecappableSession is test-seq (4): the campaign has only a running
// session and an EMPTY ended one (no transcript) → an ephemeral "no recappable
// session" reply, engine not called (findings 2a/4).
func TestRecapNoRecappableSession(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c,
		runningSession(c.ID, time.Now()),
		endedSession(c.ID, time.Now().Add(-time.Hour), 0), // ended but no lines
	)
	eng := &fakeRecapEngine{}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "no recappable session") {
		t.Errorf("no-recappable reply = %q, want a 'no recappable session' message", resp.followups[0].content)
	}
	if eng.calls != 0 {
		t.Errorf("engine ran %d times with nothing recappable, want 0", eng.calls)
	}
}

// TestRecapDefaultPickSkipsEmptyEnded is finding 2a: the default pick skips an empty
// ended row (line_count 0) on top and recaps the older ENDED row that has a
// transcript — an empty session must not hide a real recappable one.
func TestRecapDefaultPickSkipsEmptyEnded(t *testing.T) {
	c := campaign("Lost Mine")
	empty := endedSession(c.ID, time.Date(2026, 6, 26, 18, 0, 0, 0, time.UTC), 0)
	real := endedSession(c.ID, time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC), 55)
	store := recapStore(c, empty, real) // newest-first: empty on top
	eng := &fakeRecapEngine{result: recap.Result{Text: "recap"}}
	dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if eng.calls != 1 || len(eng.gotIDs) != 1 || eng.gotIDs[0] != real.ID {
		t.Fatalf("engine ids = %v, want [%s] (the older NON-empty ended session)", eng.gotIDs, real.ID)
	}
}

// TestRecapEngineErrorRepliesGeneric is test-seq (5): an UNEXPECTED engine failure
// surfaces the generic friendly ephemeral message via Followup (the ACK is already
// sent), carries NO raw error text, and the handler returns the wrapped error for
// the log.
func TestRecapEngineErrorRepliesGeneric(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{err: errors.New("llm 500")}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if len(resp.replies) != 0 {
		t.Errorf("post-Defer error must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	got := resp.followups[0].content
	if !strings.Contains(strings.ToLower(got), "went wrong") {
		t.Errorf("engine-error reply = %q, want the generic failure message", got)
	}
	if strings.Contains(got, "llm 500") {
		t.Errorf("engine-error reply leaked the raw error: %q", got)
	}
}

// TestRecapTimeoutRepliesFriendly is finding 5b: when the recap runs past
// recapOpTimeout the engine's ctx is cancelled; the handler surfaces a friendly
// "took too long" ephemeral reply (no raw error). Shrinking recapOpTimeout keeps the
// test fast; DELETING the context.WithTimeout wrap in handleRecap would leave the ctx
// deadline-less and hang this test (a blocking engine never returns) — proving the
// timeout is load-bearing.
func TestRecapTimeoutRepliesFriendly(t *testing.T) {
	old := recapOpTimeout
	recapOpTimeout = 30 * time.Millisecond
	defer func() { recapOpTimeout = old }()

	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{block: true}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "too long") {
		t.Errorf("timeout reply = %q, want a friendly 'took too long' message", resp.followups[0].content)
	}
}

// TestRecapOversizedSplitsFollowups is test-seq (6): a recap over Discord's
// 2000-char cap is delivered as MULTIPLE Followups, each at most 2000 RUNES (not
// bytes — the recap is multibyte German), in order, all the same visibility, and
// never truncated (every non-whitespace character is preserved).
func TestRecapOversizedSplitsFollowups(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 400))
	// ~4500 runes of multibyte text with newline + space break points.
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "Ödland Zeile %d über die Höhle\n", i)
	}
	long := sb.String()
	if len([]rune(long)) <= discordMessageLimit {
		t.Fatalf("fixture is only %d runes, want > %d", len([]rune(long)), discordMessageLimit)
	}
	eng := &fakeRecapEngine{result: recap.Result{Text: long}}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, nil)

	if len(resp.followups) < 2 {
		t.Fatalf("oversized recap produced %d Followups, want >= 2", len(resp.followups))
	}
	firstVis := resp.followups[0].ephemeral
	var joined strings.Builder
	for i, f := range resp.followups {
		if n := len([]rune(f.content)); n > discordMessageLimit {
			t.Errorf("Followup %d is %d runes, want <= %d", i, n, discordMessageLimit)
		}
		if f.ephemeral != firstVis {
			t.Errorf("Followup %d visibility = %v, want all %v (same visibility)", i, f.ephemeral, firstVis)
		}
		joined.WriteString(f.content)
	}
	if stripSpace(joined.String()) != stripSpace(long) {
		t.Error("re-joined Followups do not reproduce the recap (content lost or reordered)")
	}
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' {
			return -1
		}
		return r
	}, s)
}

// assertPublicDelivery encodes the Discord rule (finding 1): a public recap must
// EDIT the ephemeral placeholder FIRST (so the channel never sees a dangling public
// "thinking…"), THEN post the recap as real PUBLIC followups. It fails if the
// placeholder edit isn't ephemeral, or if any recap-body message leaked out
// ephemeral (the exact regression the wrong followup-only mechanism caused). Returns
// the joined public body for content assertions.
func assertPublicDelivery(t *testing.T, resp *fakeResponder) string {
	t.Helper()
	if resp.deferred == nil || !*resp.deferred {
		t.Fatalf("recap always Defers ephemerally; deferred = %v", resp.deferred)
	}
	if len(resp.followups) < 2 {
		t.Fatalf("public delivery must consume the placeholder THEN post public followups; got %+v", resp.followups)
	}
	if !resp.followups[0].ephemeral || resp.followups[0].kind != kindEdit {
		t.Errorf("the placeholder-consuming message must be an ephemeral EditOriginal (no public dangle); got %+v", resp.followups[0])
	}
	var b strings.Builder
	for _, f := range resp.followups[1:] {
		if f.ephemeral {
			t.Errorf("recap body must be delivered PUBLIC, but a body message is ephemeral: %q", f.content)
		}
		if f.kind != kindFollowup {
			t.Errorf("recap body must be a real followup, not a placeholder edit: %+v", f)
		}
		b.WriteString(f.content)
	}
	return b.String()
}

// TestRecapDeliveryPublic is test-seq (7a): delivery=public → ephemeral Defer, the
// placeholder consumed by an ephemeral edit, then the recap prose in a PUBLIC
// Followup the channel can see.
func TestRecapDeliveryPublic(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{result: recap.Result{Text: "The keep fell at dawn."}}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"delivery": deliveryPublic})

	body := assertPublicDelivery(t, resp)
	if !strings.Contains(body, "keep fell") {
		t.Errorf("public body = %q, want the recap prose", body)
	}
}

// TestRecapVoicedDegradesToPublic is test-seq (7b): delivery=voiced with a nil
// ButlerVoicer (v1: the Butler is never voiced) degrades to PUBLIC text prefixed
// with the degrade hint — placeholder consumed ephemerally, prose public.
func TestRecapVoicedDegradesToPublic(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{result: recap.Result{Text: "The keep fell at dawn."}}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"delivery": deliveryVoiced})

	body := assertPublicDelivery(t, resp)
	if !strings.HasPrefix(body, voicedDegradeHint) {
		t.Errorf("degraded public body = %q, want it prefixed with the degrade hint", body)
	}
	if !strings.Contains(body, "keep fell") {
		t.Errorf("degraded public body = %q, want it still carrying the recap prose", body)
	}
}

// TestRecapVoicedButlerErrorDegradesToPublic pins #365 review finding 2: when a
// wired ButlerVoicer is picked (live session) but SpeakAsButler FAILS — a voiceless
// Butler (ErrButlerVoiceless), or the session ending during the up-to-120s recap
// (ErrNoActiveSession) — the finished recap is NOT discarded and the GM is NOT told
// it was voiced. It degrades to the SAME public-text-with-hint path as an unwired
// voicer, so the room gets the recap and no phantom "voiced" claim is made.
func TestRecapVoicedButlerErrorDegradesToPublic(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	store.byID = map[uuid.UUID]storage.VoiceSession{}
	store.fakeSessionStore.byID = map[uuid.UUID]storage.Campaign{c.ID: c}
	eng := &fakeRecapEngine{result: recap.Result{Text: "The keep fell at dawn."}}
	voicer := &fakeButlerVoicer{err: errors.New("butler has no voice")}
	live := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: c.ID}}

	resp := dispatchRecap(t, store, live, eng, voicer, map[string]string{"delivery": deliveryVoiced})

	body := assertPublicDelivery(t, resp)
	if !strings.HasPrefix(body, voicedDegradeHint) {
		t.Errorf("degraded public body = %q, want the degrade hint prefix", body)
	}
	if !strings.Contains(body, "keep fell") {
		t.Errorf("degraded public body = %q, want it still carrying the recap prose", body)
	}
}

// TestRecapOversizedPublicSplits (finding 5c / oversized-public): an over-length
// PUBLIC recap consumes the placeholder once (ephemeral), then posts MULTIPLE public
// Followups each ≤2000 runes, in order, never truncated.
func TestRecapOversizedPublicSplits(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 400))
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "Ödland Zeile %d über die Höhle\n", i)
	}
	long := sb.String()
	eng := &fakeRecapEngine{result: recap.Result{Text: long}}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"delivery": deliveryPublic})

	body := assertPublicDelivery(t, resp)
	// >= 2 public body messages (placeholder edit + at least two parts => >= 3 total).
	if len(resp.followups) < 3 {
		t.Fatalf("oversized public recap = %d messages, want >= 3 (edit + multiple public parts)", len(resp.followups))
	}
	for i, f := range resp.followups[1:] {
		if n := len([]rune(f.content)); n > discordMessageLimit {
			t.Errorf("public part %d is %d runes, want <= %d", i, n, discordMessageLimit)
		}
	}
	if stripSpace(body) != stripSpace(long) {
		t.Error("re-joined public parts do not reproduce the recap (content lost or reordered)")
	}
}

// TestRecapExplicitSessionOwnCampaign is test-seq (8a): an explicit `session` UUID
// belonging to the Active Campaign is the one recapped.
func TestRecapExplicitSessionOwnCampaign(t *testing.T) {
	c := campaign("Lost Mine")
	latest := endedSession(c.ID, time.Date(2026, 6, 26, 18, 0, 0, 0, time.UTC), 88)
	older := endedSession(c.ID, time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC), 40)
	store := recapStore(c, latest, older)
	eng := &fakeRecapEngine{result: recap.Result{Text: "recap"}}
	// Explicitly pick the OLDER session, not the default latest.
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"session": older.ID.String()})

	if eng.calls != 1 || len(eng.gotIDs) != 1 || eng.gotIDs[0] != older.ID {
		t.Fatalf("engine ids = %v, want [%s] (the explicitly chosen session)", eng.gotIDs, older.ID)
	}
	if len(resp.followups) != 1 {
		t.Errorf("want one Followup, got %+v", resp.followups)
	}
}

// TestRecapExplicitSessionRejected is test-seq (8b): a foreign-campaign UUID or an
// unparsable value → an ephemeral error, and the engine is NOT called.
func TestRecapExplicitSessionRejected(t *testing.T) {
	c := campaign("Lost Mine")
	foreign := endedSession(uuid.New(), time.Now(), 5) // a different campaign's session
	cases := map[string]string{
		"foreign campaign": foreign.ID.String(),
		"unparsable":       "not-a-uuid",
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			store := recapStore(c, endedSession(c.ID, time.Now(), 12))
			store.byID[foreign.ID] = foreign // make the foreign session loadable
			eng := &fakeRecapEngine{}
			resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"session": opt})

			if eng.calls != 0 {
				t.Errorf("engine ran %d times for a rejected session, want 0", eng.calls)
			}
			if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
				t.Fatalf("want one ephemeral error Followup, got %+v", resp.followups)
			}
		})
	}
}

// TestRecapAutocompleteEndedOnly is test-seq (9): the session autocomplete offers
// ENDED sessions only, labelled "2006-01-02 15:04 · N lines", each carrying the
// session UUID as its value.
func TestRecapAutocompleteEndedOnly(t *testing.T) {
	c := campaign("Lost Mine")
	ended := endedSession(c.ID, time.Date(2026, 6, 26, 18, 30, 0, 0, time.UTC), 88)
	running := runningSession(c.ID, time.Date(2026, 6, 27, 18, 0, 0, 0, time.UTC))
	store := recapStore(c, running, ended)
	cmd := RecapCommand(store, &fakeVoice{}, &fakeRecapEngine{}, nil)

	choices, err := cmd.Autocomplete(context.Background(), &Autocomplete{userID: operatorID, guildID: testGuild})
	if err != nil {
		t.Fatalf("autocomplete: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("choices = %d, want 1 (ended only, the running session excluded)", len(choices))
	}
	ch := choices[0].(discord.AutocompleteChoiceString)
	if ch.Value != ended.ID.String() {
		t.Errorf("choice value = %q, want the ended session UUID %s", ch.Value, ended.ID)
	}
	if ch.Name != "2026-06-26 18:30 · 88 lines" {
		t.Errorf("choice label = %q, want \"2026-06-26 18:30 · 88 lines\"", ch.Name)
	}
}

// TestRecapAutocompleteCapsAtDiscordLimit: even with many ended sessions the picker
// never offers more than Discord's 25-choice cap.
func TestRecapAutocompleteCapsAtDiscordLimit(t *testing.T) {
	c := campaign("Lost Mine")
	var sessions []storage.VoiceSession
	for i := 0; i < 40; i++ {
		sessions = append(sessions, endedSession(c.ID, time.Now().Add(-time.Duration(i)*time.Hour), i))
	}
	store := recapStore(c, sessions...)
	cmd := RecapCommand(store, &fakeVoice{}, &fakeRecapEngine{}, nil)

	choices, err := cmd.Autocomplete(context.Background(), &Autocomplete{userID: operatorID, guildID: testGuild})
	if err != nil {
		t.Fatalf("autocomplete: %v", err)
	}
	if len(choices) > discordChoiceLimit {
		t.Errorf("choices = %d, want <= %d (Discord cap)", len(choices), discordChoiceLimit)
	}
}

// TestRecapNonOperatorDenied: a non-allowlisted user's /glyphoxa recap is denied by
// the Gate before the handler runs (GM-only, ADR-0041) — the engine never runs.
func TestRecapNonOperatorDenied(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(RecapCommand(store, &fakeVoice{}, eng, nil))
	resp := dispatchAs(reg, "glyphoxa recap", strangerID, nil)

	if eng.calls != 0 {
		t.Errorf("engine ran for a non-operator, want 0 (GM-only gate)")
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("denial = %+v, want one ephemeral reply", resp.replies)
	}
}

// TestRecapVoicedHappyPath is finding 5a: delivery=voiced with a wired ButlerVoicer
// AND a live session actually voices the recap — SpeakAsButler is called with the
// recap prose and the GM gets an ephemeral confirmation (no public text dump).
func TestRecapVoicedHappyPath(t *testing.T) {
	c := campaign("Lost Mine")
	store := recapStore(c, endedSession(c.ID, time.Now(), 12))
	eng := &fakeRecapEngine{result: recap.Result{Text: "The keep fell at dawn."}}
	voicer := &fakeButlerVoicer{}
	live := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: c.ID}}
	// A live session pins the Active Campaign to c, so seed its lookup too.
	store.byID = map[uuid.UUID]storage.VoiceSession{}
	store.fakeSessionStore.byID = map[uuid.UUID]storage.Campaign{c.ID: c}

	resp := dispatchRecap(t, store, live, eng, voicer, map[string]string{"delivery": deliveryVoiced})

	if voicer.calls != 1 || voicer.spoken != "The keep fell at dawn." {
		t.Fatalf("SpeakAsButler = (calls %d, %q), want (1, the recap prose)", voicer.calls, voicer.spoken)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral confirmation Followup, got %+v", resp.followups)
	}
	if strings.Contains(resp.followups[0].content, "The keep fell") {
		t.Errorf("voiced confirmation should NOT dump the prose as text; got %q", resp.followups[0].content)
	}
}

// TestRecapVoicedForeignTenantSessionDegradesToText pins the cross-tenant voiced
// guard (#490): the Manager is single-active, so when the LIVE session belongs to
// another Tenant a `voiced` recap must NOT be spoken into that Tenant's channel
// (SpeakAsButler) nor persisted as a KindButler line there — it degrades to public
// text instead. The invoking Tenant's Active Campaign is its own durable selection.
func TestRecapVoicedForeignTenantSessionDegradesToText(t *testing.T) {
	mine := campaignIn(tenantA, "My Campaign") // durable selection (tenantA = testGuild)
	foreign := campaignIn(tenantB, "Foreign live")
	ended := endedSession(mine.ID, time.Now(), 12) // recappable session of MY campaign
	store := &fakeRecapStore{
		fakeSessionStore: &fakeSessionStore{
			forUser: &mine,
			byID:    map[uuid.UUID]storage.Campaign{mine.ID: mine, foreign.ID: foreign},
		},
		sessions: []storage.VoiceSession{ended},
		byID:     map[uuid.UUID]storage.VoiceSession{ended.ID: ended},
	}
	eng := &fakeRecapEngine{result: recap.Result{Text: "The keep fell at dawn."}}
	voicer := &fakeButlerVoicer{}
	// A live session for ANOTHER Tenant. Active is Tenant-keyed (#488), so the tenantA
	// recap query sees no session here → the voiced request degrades to public text.
	live := &fakeVoice{active: true, tenantID: tenantB, snap: storage.VoiceSession{CampaignID: foreign.ID}}

	resp := dispatchRecap(t, store, live, eng, voicer, map[string]string{"delivery": deliveryVoiced})

	if voicer.calls != 0 {
		t.Fatalf("SpeakAsButler called %d times — a foreign Tenant's channel was voiced", voicer.calls)
	}
	// Degraded to public text: the recap prose lands as a public Followup.
	var sawPublicProse bool
	for _, f := range resp.followups {
		if !f.ephemeral && strings.Contains(f.content, "The keep fell at dawn.") {
			sawPublicProse = true
		}
	}
	if !sawPublicProse {
		t.Errorf("recap not delivered as public text after the foreign-tenant degrade; followups = %+v", resp.followups)
	}
}

// TestRecapPublicErrorStaysEphemeral is finding 5c: delivery=public with an error
// path (no Active Campaign) must NOT leave a public reply — the Defer is ephemeral
// and the error reply is ephemeral, so no dangling public "thinking…" for the
// channel.
func TestRecapPublicErrorStaysEphemeral(t *testing.T) {
	store := &fakeRecapStore{fakeSessionStore: &fakeSessionStore{}} // no Active Campaign
	eng := &fakeRecapEngine{}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"delivery": deliveryPublic})

	if resp.deferred == nil || !*resp.deferred {
		t.Fatalf("Defer must be ephemeral even for delivery=public; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 0 {
		t.Errorf("post-Defer error must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("public-delivery error must reply ONE EPHEMERAL Followup (no public dangle), got %+v", resp.followups)
	}
	if eng.calls != 0 {
		t.Errorf("engine ran %d times on the no-campaign path, want 0", eng.calls)
	}
}

// TestRecapExplicitEmptyTranscript is finding 2b: an explicit session id whose recap
// yields recap.ErrNoTranscript is a NORMAL state — a friendly ephemeral "no
// transcript" reply, NOT the generic "went wrong" failure.
func TestRecapExplicitEmptyTranscript(t *testing.T) {
	c := campaign("Lost Mine")
	// A running session the GM explicitly targets (allowed) that has no lines yet.
	live := runningSession(c.ID, time.Now())
	store := recapStore(c, live)
	eng := &fakeRecapEngine{err: recap.ErrNoTranscript}
	resp := dispatchRecap(t, store, &fakeVoice{}, eng, nil, map[string]string{"session": live.ID.String()})

	if eng.calls != 1 {
		t.Fatalf("engine calls = %d, want 1 (an explicit running id is recappable)", eng.calls)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	got := strings.ToLower(resp.followups[0].content)
	if !strings.Contains(got, "no transcript") {
		t.Errorf("empty-transcript reply = %q, want a 'no transcript' message", resp.followups[0].content)
	}
	if strings.Contains(got, "went wrong") {
		t.Errorf("empty transcript must not be a generic failure; got %q", resp.followups[0].content)
	}
}
