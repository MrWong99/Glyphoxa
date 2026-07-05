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

// fakeTranscriptSearch records the campaign + query the handler resolved and
// returns canned lines/errors, so the scope precedence and formatting can be
// asserted without a DB.
type fakeTranscriptSearch struct {
	lines       []storage.TranscriptLine
	searchErr   error
	campaign    storage.Campaign
	campaignErr error

	gotCampaign    uuid.UUID
	gotQuery       string
	gotLimit       int
	searchCalls    int
	getActiveCalls int
}

func (f *fakeTranscriptSearch) SearchTranscriptLines(_ context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error) {
	f.searchCalls++
	f.gotCampaign = campaignID
	f.gotQuery = query
	f.gotLimit = limit
	return f.lines, f.searchErr
}

func (f *fakeTranscriptSearch) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	f.getActiveCalls++
	if f.campaignErr != nil {
		return storage.Campaign{}, f.campaignErr
	}
	return f.campaign, nil
}

// dispatchSearch registers the SearchCommand on a GM-configured registry and
// dispatches it as the allowlisted operator with the given query, returning the
// recorded responder + the fake. The GM path is exercised end-to-end (auth gate,
// Defer watchdog, reply routing).
func dispatchSearch(t *testing.T, fake *fakeTranscriptSearch, activeCampaign func() (uuid.UUID, bool), query string) *fakeResponder {
	t.Helper()
	reg := testRegistry(testGuild, operatorID)
	reg.Register(SearchCommand(fake, activeCampaign))
	resp := &fakeResponder{}
	ic := &Interaction{
		guildID: testGuild,
		userID:  operatorID,
		opts:    fakeOpts{s: map[string]string{"query": query}},
		resp:    resp,
	}
	reg.dispatch(context.Background(), "glyphoxa search", ic)
	return resp
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
	campaignID := uuid.New()
	fake := &fakeTranscriptSearch{
		campaign: storage.Campaign{ID: campaignID},
		lines: []storage.TranscriptLine{
			bartLine(campaignID),
			{VoiceSessionID: uuid.New(), CampaignID: campaignID, LineID: "u:1", Seq: 1, Who: "Player / DM", Kind: "player", TS: time.Date(2026, 6, 27, 18, 0, 1, 0, time.UTC), Text: "Where is the dragon?"},
		},
	}
	resp := dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "dragon")

	if resp.deferred == nil || !*resp.deferred {
		t.Fatalf("search must Defer ephemerally before the DB round trip; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 0 {
		t.Errorf("a deferred handler must not CreateMessage; replies = %+v", resp.replies)
	}
	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if fake.searchCalls != 1 || fake.gotQuery != "dragon" || fake.gotCampaign != campaignID {
		t.Errorf("search call = (calls %d, query %q, campaign %s), want (1, dragon, %s)", fake.searchCalls, fake.gotQuery, fake.gotCampaign, campaignID)
	}
	if fake.gotLimit != transcriptSearchLimit {
		t.Errorf("search limit = %d, want %d", fake.gotLimit, transcriptSearchLimit)
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
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: uuid.New()}}
	resp := dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "unicorn")

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
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: uuid.New()}}
	resp := dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "   ")

	if resp.deferred != nil {
		t.Errorf("empty query must not Defer; deferred = %v", resp.deferred)
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("empty query = %+v, want one ephemeral Reply hint", resp.replies)
	}
	if !strings.Contains(strings.ToLower(resp.replies[0].content), "search for") {
		t.Errorf("empty-query hint = %q, want a search-for hint", resp.replies[0].content)
	}
	if fake.searchCalls != 0 || fake.getActiveCalls != 0 {
		t.Errorf("empty query touched storage: search=%d getActive=%d, want 0/0", fake.searchCalls, fake.getActiveCalls)
	}
}

// TestSearchCommandNoActiveCampaign: with no live session and no stored Active
// Campaign, the reply points the operator at /glyphoxa use — and no search runs.
func TestSearchCommandNoActiveCampaign(t *testing.T) {
	fake := &fakeTranscriptSearch{campaignErr: storage.ErrNotFound}
	resp := dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "dragon")

	if len(resp.followups) != 1 || !resp.followups[0].ephemeral {
		t.Fatalf("want one ephemeral Followup, got %+v", resp.followups)
	}
	if !strings.Contains(resp.followups[0].content, "/glyphoxa use") {
		t.Errorf("no-campaign reply = %q, want the /glyphoxa use hint", resp.followups[0].content)
	}
	if fake.searchCalls != 0 {
		t.Errorf("search ran %d times with no Active Campaign, want 0", fake.searchCalls)
	}
}

// TestSearchCommandPrefersLiveSessionCampaign: while a session is live the search
// scopes to the live campaign and never falls back to GetActiveCampaign (AC5
// scope precedence, matching the web RPC).
func TestSearchCommandPrefersLiveSessionCampaign(t *testing.T) {
	live := uuid.New()
	other := uuid.New()
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: other}, lines: []storage.TranscriptLine{bartLine(live)}}
	dispatchSearch(t, fake, func() (uuid.UUID, bool) { return live, true }, "dragon")

	if fake.gotCampaign != live {
		t.Errorf("searched campaign = %s, want the live session's %s (not GetActiveCampaign %s)", fake.gotCampaign, live, other)
	}
	if fake.getActiveCalls != 0 {
		t.Errorf("GetActiveCampaign called %d times while a session was live, want 0", fake.getActiveCalls)
	}
}

// TestSearchCommandFallsBackToActiveCampaign: with no live session it resolves the
// stored Active Campaign.
func TestSearchCommandFallsBackToActiveCampaign(t *testing.T) {
	stored := uuid.New()
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: stored}, lines: []storage.TranscriptLine{bartLine(stored)}}
	dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "dragon")

	if fake.gotCampaign != stored {
		t.Errorf("searched campaign = %s, want the stored Active Campaign %s", fake.gotCampaign, stored)
	}
	if fake.getActiveCalls != 1 {
		t.Errorf("GetActiveCampaign called %d times, want 1 (the fallback)", fake.getActiveCalls)
	}
}

// TestSearchCommandStorageErrorRepliesGeneric: a storage failure returns an error
// from the handler, which the Registry answers with the generic ephemeral reply
// via Followup (post-Defer), never leaving the interaction on "thinking…".
func TestSearchCommandStorageErrorRepliesGeneric(t *testing.T) {
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: uuid.New()}, searchErr: context.DeadlineExceeded}
	resp := dispatchSearch(t, fake, func() (uuid.UUID, bool) { return uuid.Nil, false }, "dragon")

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
	cmd := SearchCommand(&fakeTranscriptSearch{}, nil)
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
	fake := &fakeTranscriptSearch{campaign: storage.Campaign{ID: uuid.New()}}
	reg := testRegistry(testGuild, operatorID)
	reg.Register(SearchCommand(fake, func() (uuid.UUID, bool) { return uuid.Nil, false }))
	resp := &fakeResponder{}
	reg.dispatch(context.Background(), "glyphoxa search", &Interaction{
		guildID: testGuild, userID: strangerID,
		opts: fakeOpts{s: map[string]string{"query": "dragon"}}, resp: resp,
	})

	if fake.searchCalls != 0 {
		t.Errorf("search ran for a non-operator, want 0 (GM-only gate)")
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Fatalf("denial = %+v, want one ephemeral reply", resp.replies)
	}
}
