package presence

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeSearchStore reuses #216's fakeSessionStore (the shared slash Active-Campaign
// resolver) and adds the transcript search path, recording the campaign + query
// the handler resolved so the scope precedence can be asserted without a DB.
type fakeSearchStore struct {
	*fakeSessionStore
	lines       []storage.TranscriptLine
	searchErr   error
	gotCampaign uuid.UUID
	gotQuery    string
	gotLimit    int
	searchCalls int
}

func (f *fakeSearchStore) SearchTranscriptLines(_ context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error) {
	f.searchCalls++
	f.gotCampaign = campaignID
	f.gotQuery = query
	f.gotLimit = limit
	return f.lines, f.searchErr
}

// selectionStore is a fakeSearchStore whose Active Campaign resolves via the
// operator's durable /glyphoxa use selection (the common case for these tests).
func selectionStore(selected storage.Campaign, lines ...storage.TranscriptLine) *fakeSearchStore {
	return &fakeSearchStore{
		fakeSessionStore: &fakeSessionStore{forUser: &selected},
		lines:            lines,
	}
}

// dispatchSearch registers /glyphoxa search on a GM-configured registry and
// dispatches it as the allowlisted operator with the given query, exercising the
// full GM path (auth gate, Defer watchdog, reply routing).
func dispatchSearch(t *testing.T, store SearchStore, voice VoiceControl, query string) *fakeResponder {
	t.Helper()
	reg := testRegistry(testGuild, operatorID)
	reg.Register(SearchCommand(store, voice))
	return dispatchAs(reg, "glyphoxa search", operatorID, map[string]string{"query": query})
}

func bartLine(campaignID uuid.UUID) storage.TranscriptLine {
	return storage.TranscriptLine{
		VoiceSessionID: uuid.New(), CampaignID: campaignID, LineID: "a:t1", Seq: 2,
		Who: "Bart", Tag: "NPC", Kind: "npc",
		TS: time.Date(2026, 6, 27, 18, 0, 2, 0, time.UTC), Text: "Well met, traveller.",
	}
}

// TestSearchCommandQuotesTopMatches is #120 AC3: /glyphoxa search Defers, calls the
// shared storage path with the resolved campaign + raw query + small limit, and
// quotes the top matches with speaker + timestamp in ONE ephemeral Followup.
func TestSearchCommandQuotesTopMatches(t *testing.T) {
	c := campaign("Lost Mine")
	store := selectionStore(c,
		bartLine(c.ID),
		storage.TranscriptLine{VoiceSessionID: uuid.New(), CampaignID: c.ID, LineID: "u:1", Seq: 1, Who: "Player / DM", Kind: "player", TS: time.Date(2026, 6, 27, 18, 0, 1, 0, time.UTC), Text: "Where is the dragon?"},
	)
	resp := dispatchSearch(t, store, &fakeVoice{}, "dragon")

	if resp.deferred == nil || !*resp.deferred {
		t.Fatalf("search must Defer ephemerally before the DB round trip; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 0 {
		t.Errorf("a deferred handler must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if store.searchCalls != 1 || store.gotQuery != "dragon" || store.gotCampaign != c.ID {
		t.Errorf("search call = (calls %d, query %q, campaign %s), want (1, dragon, %s)", store.searchCalls, store.gotQuery, store.gotCampaign, c.ID)
	}
	if store.gotLimit != transcriptSearchLimit {
		t.Errorf("search limit = %d, want %d", store.gotLimit, transcriptSearchLimit)
	}
	body := resp.followups[0].content
	for _, want := range []string{"Bart (NPC)", "18:00:02", "Well met, traveller.", "Player / DM", "Where is the dragon?"} {
		if !strings.Contains(body, want) {
			t.Errorf("reply missing %q; got:\n%s", want, body)
		}
	}
}

// TestSearchCommandNoMatches is #120 AC3's no-match case: a clear ephemeral reply
// quoting the query.
func TestSearchCommandNoMatches(t *testing.T) {
	resp := dispatchSearch(t, selectionStore(campaign("Lost Mine")), &fakeVoice{}, "unicorn")

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if got := resp.followups[0].content; got != `No lines match "unicorn".` {
		t.Errorf("no-match reply = %q, want the clear no-match line", got)
	}
}

// TestSearchCommandEmptyQuery: a blank/whitespace query short-circuits with a
// hint, WITHOUT Deferring or touching the DB.
func TestSearchCommandEmptyQuery(t *testing.T) {
	store := selectionStore(campaign("Lost Mine"))
	resp := dispatchSearch(t, store, &fakeVoice{}, "   ")

	if resp.deferred != nil {
		t.Errorf("empty query must not Defer; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("empty query = %+v, want one ephemeral Reply hint", resp.replies)
	}
	if !strings.Contains(strings.ToLower(resp.replies[0].content), "search for") {
		t.Errorf("empty-query hint = %q, want a search-for hint", resp.replies[0].content)
	}
	if store.searchCalls != 0 {
		t.Errorf("empty query touched storage: search=%d, want 0", store.searchCalls)
	}
}

// TestSearchCommandNoActiveCampaign: with no live session and no durable /glyphoxa
// use selection, the reply points the operator at /glyphoxa use (the SHARED slash
// resolver's ErrNoActiveCampaign, no most-recent fallback) — and no search runs.
func TestSearchCommandNoActiveCampaign(t *testing.T) {
	store := &fakeSearchStore{fakeSessionStore: &fakeSessionStore{}} // forUser nil -> ErrNotFound
	resp := dispatchSearch(t, store, &fakeVoice{}, "dragon")

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(resp.followups[0].content, "/glyphoxa use") {
		t.Errorf("no-campaign reply = %q, want the /glyphoxa use hint", resp.followups[0].content)
	}
	if store.searchCalls != 0 {
		t.Errorf("search ran %d times with no Active Campaign, want 0", store.searchCalls)
	}
}

// TestSearchCommandResolvesLiveSessionCampaign: while a session is live the search
// scopes to the LIVE session's campaign (the shared resolver's first choice), NOT
// the durable selection — matching /glyphoxa start (AC5 scope precedence).
func TestSearchCommandResolvesLiveSessionCampaign(t *testing.T) {
	live := campaign("Live")
	other := campaign("Other")
	store := &fakeSearchStore{
		fakeSessionStore: &fakeSessionStore{
			byID:    map[uuid.UUID]storage.Campaign{live.ID: live},
			forUser: &other, // a different durable selection; the live session must win
		},
		lines: []storage.TranscriptLine{bartLine(live.ID)},
	}
	voice := &fakeVoice{active: true, snap: storage.VoiceSession{CampaignID: live.ID}}
	dispatchSearch(t, store, voice, "dragon")

	if store.gotCampaign != live.ID {
		t.Errorf("searched campaign = %s, want the live session's %s (not the durable selection %s)", store.gotCampaign, live.ID, other.ID)
	}
}

// TestSearchCommandFallsBackToDurableSelection: with no live session it resolves
// the operator's durable /glyphoxa use selection.
func TestSearchCommandFallsBackToDurableSelection(t *testing.T) {
	selected := campaign("Selected")
	store := selectionStore(selected, bartLine(selected.ID))
	dispatchSearch(t, store, &fakeVoice{}, "dragon")

	if store.gotCampaign != selected.ID {
		t.Errorf("searched campaign = %s, want the durable selection %s", store.gotCampaign, selected.ID)
	}
}

// TestSearchCommandStorageErrorRepliesGeneric: a storage failure returns an error
// from the handler, which the Registry answers with the generic ephemeral reply
// via Followup (post-Defer), never leaving the interaction on "thinking…".
func TestSearchCommandStorageErrorRepliesGeneric(t *testing.T) {
	store := selectionStore(campaign("Lost Mine"))
	store.searchErr = context.DeadlineExceeded
	resp := dispatchSearch(t, store, &fakeVoice{}, "dragon")

	if len(resp.replies) != 0 {
		t.Errorf("post-Defer error must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(strings.ToLower(resp.followups[0].content), "went wrong") {
		t.Errorf("storage-error reply = %q, want the generic failure message", resp.followups[0].content)
	}
}

// TestSearchCommandIsGMOnly pins the command's surface: it is the grouped
// /glyphoxa search, GM-only (ADR-0010), with a required query option.
func TestSearchCommandIsGMOnly(t *testing.T) {
	cmd := SearchCommand(&fakeSearchStore{fakeSessionStore: &fakeSessionStore{}}, &fakeVoice{})
	if cmd.Path != "glyphoxa search" {
		t.Errorf("Path = %q, want \"glyphoxa search\"", cmd.Path)
	}
	if !cmd.GMOnly {
		t.Error("GMOnly = false, want true (transcript search is GM-only, ADR-0010)")
	}
	if len(cmd.Options) != 1 {
		t.Fatalf("Options = %d, want 1 (query)", len(cmd.Options))
	}
	opt, ok := cmd.Options[0].(discord.ApplicationCommandOptionString)
	if !ok || opt.Name != "query" || !opt.Required {
		t.Errorf("option = %+v, want a required string 'query'", cmd.Options[0])
	}
}

// TestSearchCommandNonOperatorDenied: a non-allowlisted user's /glyphoxa search is
// denied by the Gate before the handler runs (GM-only, ADR-0041) — no search.
func TestSearchCommandNonOperatorDenied(t *testing.T) {
	store := selectionStore(campaign("Lost Mine"))
	reg := testRegistry(testGuild, operatorID)
	reg.Register(SearchCommand(store, &fakeVoice{}))
	resp := dispatchAs(reg, "glyphoxa search", strangerID, map[string]string{"query": "dragon"})

	if store.searchCalls != 0 {
		t.Errorf("search ran for a non-operator, want 0 (GM-only gate)")
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("denial = %+v, want one ephemeral reply", resp.replies)
	}
}

// TestSearchCommandTruncatesLongLine (#120 review): a coalesced Agent reply can be
// long; the quoted line is truncated (…) so it never blows Discord's 2000-char
// content cap and 400s the Followup (which would hide ALL matches from the GM).
func TestSearchCommandTruncatesLongLine(t *testing.T) {
	c := campaign("Lost Mine")
	long := strings.Repeat("dragon fire ", 60) // ~720 chars, well over the per-line cap
	store := selectionStore(c, storage.TranscriptLine{
		VoiceSessionID: uuid.New(), CampaignID: c.ID, LineID: "a:t1", Seq: 1, Who: "Bart", Tag: "NPC", Kind: "npc",
		TS: time.Date(2026, 6, 27, 18, 0, 1, 0, time.UTC), Text: long,
	})
	resp := dispatchSearch(t, store, &fakeVoice{}, "dragon")

	if len(resp.followups) != 1 {
		t.Fatalf("want one Followup, got %+v", resp.followups)
	}
	body := resp.followups[0].content
	if !strings.Contains(body, "…") {
		t.Errorf("long line was not truncated (no ellipsis); got:\n%s", body)
	}
	if strings.Contains(body, long) {
		t.Errorf("reply carries the full untruncated line; got len %d", len([]rune(body)))
	}
	if n := len([]rune(body)); n > discordMessageLimit {
		t.Errorf("reply is %d runes, want <= %d (Discord cap)", n, discordMessageLimit)
	}
}

// TestFormatTranscriptMatchesCapsTotalLength (#120 review): even 5 pathologically
// long lines never produce a reply over the Discord content cap — a line that would
// push it over is dropped, so the message always sends.
func TestFormatTranscriptMatchesCapsTotalLength(t *testing.T) {
	lines := make([]storage.TranscriptLine, transcriptSearchLimit)
	for i := range lines {
		lines[i] = storage.TranscriptLine{
			Who:  strings.Repeat("W", 900), // long speaker label, untruncated, forces the total-cap break
			Kind: "npc",
			TS:   time.Date(2026, 6, 27, 18, 0, i, 0, time.UTC),
			Text: strings.Repeat("x", 1000),
		}
	}
	got := formatTranscriptMatches("q", lines)
	if len(got) > discordMessageLimit {
		t.Errorf("formatted reply is %d bytes, want <= %d (Discord cap)", len(got), discordMessageLimit)
	}
}

// TestSearchCommandGiantQueryNoMatch (#120 review): a string option can be up to
// 6000 chars; the no-match reply echoes only a truncated query, so it stays well
// under the Discord cap and never 400s.
func TestSearchCommandGiantQueryNoMatch(t *testing.T) {
	store := selectionStore(campaign("Lost Mine")) // no lines -> no match
	giant := strings.Repeat("a", 6000)
	resp := dispatchSearch(t, store, &fakeVoice{}, giant)

	if len(resp.followups) != 1 {
		t.Fatalf("want one Followup, got %+v", resp.followups)
	}
	body := resp.followups[0].content
	if !strings.Contains(body, "No lines match") || !strings.Contains(body, "…") {
		t.Errorf("giant-query no-match reply = %q, want a no-match line with a truncated (…) query", body)
	}
	if n := len([]rune(body)); n > discordMessageLimit {
		t.Errorf("no-match reply is %d runes, want <= %d (Discord cap)", n, discordMessageLimit)
	}
}
