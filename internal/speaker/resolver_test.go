package speaker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeChars is a scripted CharacterLookup: it returns the mapped Character for a
// (campaign, discordUser) pair, else storage.ErrNotFound, and counts calls.
type fakeChars struct {
	mu    sync.Mutex
	byKey map[string]storage.Character // key = campaign+"/"+discordUser
	err   error                        // when non-nil, returned for every lookup (a DB failure)
	gate  chan struct{}                // when non-nil, each lookup blocks on it before returning
	calls int
}

func (f *fakeChars) GetCharacterByDiscordUser(_ context.Context, campaignID uuid.UUID, discordUserID string) (storage.Character, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return storage.Character{}, f.err
	}
	if c, ok := f.byKey[campaignID.String()+"/"+discordUserID]; ok {
		return c, nil
	}
	return storage.Character{}, storage.ErrNotFound
}

func (f *fakeChars) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeNamer is a scripted MemberNamer: it returns a guild display name per
// snowflake, else an error, and counts calls.
type fakeNamer struct {
	mu     sync.Mutex
	byUser map[string]string
	err    error
	calls  int
}

func (f *fakeNamer) MemberDisplayName(_ context.Context, discordUserID string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if n, ok := f.byUser[discordUserID]; ok {
		return n, nil
	}
	return "", nil // no name known
}

func (f *fakeNamer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

const (
	kiraUser = "111111111111111111"
	gmUser   = "222222222222222222"
	guestID  = "333333333333333333"
)

func newTestResolver(t *testing.T, chars CharacterLookup, namer MemberNamer, allowIDs string) *Resolver {
	t.Helper()
	return NewResolver(chars, namer, auth.ParseOperatorAllowlist(allowIDs), nil)
}

func TestResolverWarmThenLookupCharacterName(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{
		campaign.String() + "/" + kiraUser: {Name: "Kira", CampaignID: campaign, DiscordUserID: kiraUser},
	}}
	r := newTestResolver(t, chars, &fakeNamer{}, "")

	r.Warm(campaign, kiraUser)
	r.wg.Wait()

	got := r.Lookup(campaign, kiraUser)
	if got.Name != "Kira" || got.GM {
		t.Fatalf("Lookup = %+v, want Name=Kira GM=false", got)
	}
	if chars.callCount() != 1 {
		t.Fatalf("character lookups = %d, want 1", chars.callCount())
	}
}

func TestResolverMissFallsBackToGuildName(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{}} // no Character mapped
	namer := &fakeNamer{byUser: map[string]string{guestID: "GuildGuest"}}
	r := newTestResolver(t, chars, namer, "")

	r.Warm(campaign, guestID)
	r.wg.Wait()

	got := r.Lookup(campaign, guestID)
	if got.Name != "GuildGuest" {
		t.Fatalf("Lookup.Name = %q, want GuildGuest", got.Name)
	}
	if namer.callCount() != 1 {
		t.Fatalf("namer calls = %d, want 1", namer.callCount())
	}
}

func TestResolverNamerErrorNegativeCachedThenExpires(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{}}
	namer := &fakeNamer{err: errors.New("discord unavailable")}
	r := newTestResolver(t, chars, namer, "")

	base := time.Now()
	r.now = func() time.Time { return base }

	r.Warm(campaign, guestID)
	r.wg.Wait()

	if got := r.Lookup(campaign, guestID); got.Name != "" {
		t.Fatalf("Lookup.Name = %q, want empty (negative)", got.Name)
	}
	// A second Warm within the 30s negative TTL must NOT re-query (cached).
	r.Warm(campaign, guestID)
	r.wg.Wait()
	if namer.callCount() != 1 {
		t.Fatalf("namer calls = %d, want 1 while negative entry fresh", namer.callCount())
	}

	// Past the 30s negative TTL, the entry has expired: a fresh Warm re-queries.
	r.now = func() time.Time { return base.Add(31 * time.Second) }
	r.Warm(campaign, guestID)
	r.wg.Wait()
	if namer.callCount() != 2 {
		t.Fatalf("namer calls = %d, want 2 after negative TTL expiry", namer.callCount())
	}
}

func TestResolverNilNamerNegativeOnMiss(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{}}
	r := NewResolver(chars, nil, auth.ParseOperatorAllowlist(""), nil) // web-only mode: no guild fallback

	r.Warm(campaign, guestID)
	r.wg.Wait()

	if got := r.Lookup(campaign, guestID); got.Name != "" {
		t.Fatalf("Lookup.Name = %q, want empty with nil namer", got.Name)
	}
}

func TestResolverGMFlagIndependentOfName(t *testing.T) {
	campaign := uuid.New()
	// gmUser is on the allowlist AND maps to a Character; kiraUser maps but is not GM.
	chars := &fakeChars{byKey: map[string]storage.Character{
		campaign.String() + "/" + gmUser:   {Name: "Dungeon Master", CampaignID: campaign, DiscordUserID: gmUser},
		campaign.String() + "/" + kiraUser: {Name: "Kira", CampaignID: campaign, DiscordUserID: kiraUser},
	}}
	r := newTestResolver(t, chars, &fakeNamer{}, gmUser)

	r.Warm(campaign, gmUser)
	r.Warm(campaign, kiraUser)
	r.wg.Wait()

	if got := r.Lookup(campaign, gmUser); !got.GM || got.Name != "Dungeon Master" {
		t.Fatalf("mapped GM Lookup = %+v, want GM=true Name=Dungeon Master", got)
	}
	if got := r.Lookup(campaign, kiraUser); got.GM || got.Name != "Kira" {
		t.Fatalf("player Lookup = %+v, want GM=false Name=Kira", got)
	}

	// Unmapped GM: no Character, no guild name — still GM by allowlist membership.
	unmappedGM := "999999999999999999"
	r2 := newTestResolver(t, &fakeChars{byKey: map[string]storage.Character{}}, &fakeNamer{}, unmappedGM)
	if got := r2.Lookup(campaign, unmappedGM); !got.GM {
		t.Fatalf("unmapped GM Lookup = %+v, want GM=true (allowlist membership, no name needed)", got)
	}
}

func TestResolverInvalidateCampaignReQueriesRebind(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{
		campaign.String() + "/" + kiraUser: {Name: "Kira", CampaignID: campaign, DiscordUserID: kiraUser},
	}}
	r := newTestResolver(t, chars, &fakeNamer{}, "")

	r.Warm(campaign, kiraUser)
	r.wg.Wait()
	if got := r.Lookup(campaign, kiraUser); got.Name != "Kira" {
		t.Fatalf("pre-rebind Name = %q, want Kira", got.Name)
	}

	// Operator rebinds the same Discord User to a new Character, then invalidates.
	chars.mu.Lock()
	chars.byKey[campaign.String()+"/"+kiraUser] = storage.Character{Name: "Kira Reborn", CampaignID: campaign, DiscordUserID: kiraUser}
	chars.mu.Unlock()

	// Without invalidation the cached "Kira" would still be served.
	if got := r.Lookup(campaign, kiraUser); got.Name != "Kira" {
		t.Fatalf("cached Name = %q, want still Kira before invalidation", got.Name)
	}

	r.InvalidateCampaign(campaign)
	if got := r.Lookup(campaign, kiraUser); got.Name != "" {
		t.Fatalf("post-invalidate Name = %q, want empty (cache dropped)", got.Name)
	}

	r.Warm(campaign, kiraUser)
	r.wg.Wait()
	if got := r.Lookup(campaign, kiraUser); got.Name != "Kira Reborn" {
		t.Fatalf("post-rebind Name = %q, want Kira Reborn", got.Name)
	}
}

func TestResolverWarmDedupesConcurrentFills(t *testing.T) {
	campaign := uuid.New()
	gate := make(chan struct{})
	chars := &fakeChars{
		byKey: map[string]storage.Character{
			campaign.String() + "/" + kiraUser: {Name: "Kira", CampaignID: campaign, DiscordUserID: kiraUser},
		},
		gate: gate,
	}
	r := newTestResolver(t, chars, &fakeNamer{}, "")

	// Fire many concurrent Warms for the SAME key while the fake lookup is blocked.
	const n = 20
	var start, done sync.WaitGroup
	start.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			start.Done()
			r.Warm(campaign, kiraUser)
		}()
	}
	start.Wait()
	done.Wait() // every Warm has returned; exactly one grabbed the inflight guard
	// Give the fill goroutine time to block on the gated lookup, then release.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	r.wg.Wait()

	if got := chars.callCount(); got != 1 {
		t.Fatalf("character lookups = %d, want 1 (deduped)", got)
	}
	if got := r.Lookup(campaign, kiraUser); got.Name != "Kira" {
		t.Fatalf("Lookup.Name = %q, want Kira", got.Name)
	}
}

func TestResolverLookupNeverDoesIO(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeChars{byKey: map[string]storage.Character{}}
	namer := &fakeNamer{byUser: map[string]string{guestID: "GuildGuest"}}
	r := newTestResolver(t, chars, namer, "")

	// Lookup on a cold cache must not touch the store or namer.
	_ = r.Lookup(campaign, guestID)
	if chars.callCount() != 0 || namer.callCount() != 0 {
		t.Fatalf("Lookup did I/O: chars=%d namer=%d, want 0/0", chars.callCount(), namer.callCount())
	}
}
