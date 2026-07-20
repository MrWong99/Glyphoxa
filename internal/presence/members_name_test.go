package presence

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func strptr(s string) *string { return &s }

// campaignTenantStore is the TenantStore the name tests need: it resolves a
// Campaign to its owning Tenant (the narrowing read, #483); the other reads are
// unused on this path.
type campaignTenantStore struct {
	campaigns map[uuid.UUID]storage.Campaign
}

func (s *campaignTenantStore) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, storage.ErrNotFound
}
func (s *campaignTenantStore) ListDeploymentConfigs(context.Context) ([]storage.DeploymentConfig, error) {
	return nil, nil
}
func (s *campaignTenantStore) GetTenantIDByGuildID(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, storage.ErrNotFound
}
func (s *campaignTenantStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	if c, ok := s.campaigns[id]; ok {
		return c, nil
	}
	return storage.Campaign{}, storage.ErrNotFound
}

// newNameTestPresence builds a bare Clients registry with one standing client
// serving one Tenant whose configured Guild is guildID (empty = the wait-state),
// plus a Campaign owned by that Tenant and an injected member fetch, so
// MemberDisplayName is unit-tested without a live gateway.
func newNameTestPresence(guildID string, fetch func(ctx context.Context, r rest.Rest, gid, uid snowflake.ID) (*discord.Member, error)) (*Clients, uuid.UUID) {
	tenantID, campaignID := uuid.New(), uuid.New()
	c := &Clients{
		store: &campaignTenantStore{campaigns: map[uuid.UUID]storage.Campaign{
			campaignID: {ID: campaignID, TenantID: tenantID},
		}},
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = fetch
	entry := &clientEntry{token: "tok", refs: map[uuid.UUID]struct{}{tenantID: {}}, registeredGuilds: map[string]bool{}}
	entry.client.Store(&bot.Client{})
	c.entries["tok"] = entry
	c.tenants[tenantID] = &tenantState{token: "tok", guild: guildID}
	return c, campaignID
}

func TestMemberDisplayNamePrecedence(t *testing.T) {
	const user = "111111111111111111"

	cases := []struct {
		name   string
		member *discord.Member
		want   string
	}{
		{
			name:   "nick wins over global name and username",
			member: &discord.Member{Nick: strptr("Kira the Bold"), User: discord.User{GlobalName: strptr("Kira G"), Username: "kira_u"}},
			want:   "Kira the Bold",
		},
		{
			name:   "global name wins when no nick",
			member: &discord.Member{User: discord.User{GlobalName: strptr("Kira G"), Username: "kira_u"}},
			want:   "Kira G",
		},
		{
			name:   "username when neither nick nor global name",
			member: &discord.Member{User: discord.User{Username: "kira_u"}},
			want:   "kira_u",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, campaignID := newNameTestPresence("222222222222222222", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
				return tc.member, nil
			})
			got, err := p.MemberDisplayName(context.Background(), campaignID, user)
			if err != nil {
				t.Fatalf("MemberDisplayName err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("MemberDisplayName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMemberDisplayNameScopedToOwningTenantsGuild is the epic-#483 hardening
// regression (residuals 1/g): on a shared central token serving TWO Tenants,
// the lookup must fetch ONLY the Campaign's owning Tenant's Guild — never
// another Tenant's Guild where the same Discord user may carry a different
// nickname (cross-tenant label bleed).
func TestMemberDisplayNameScopedToOwningTenantsGuild(t *testing.T) {
	const user = "111111111111111111"
	tenantA, tenantB := uuid.New(), uuid.New()
	campaignB := uuid.New() // owned by tenantB
	guildA, guildB := snowflake.ID(100), snowflake.ID(200)

	var mu sync.Mutex
	var fetched []snowflake.ID
	fetch := func(_ context.Context, _ rest.Rest, gid, _ snowflake.ID) (*discord.Member, error) {
		mu.Lock()
		fetched = append(fetched, gid)
		mu.Unlock()
		switch gid {
		case guildA:
			return &discord.Member{Nick: strptr("A-side nick")}, nil
		case guildB:
			return &discord.Member{Nick: strptr("B-side nick")}, nil
		}
		return nil, errors.New("unknown guild")
	}

	c := &Clients{
		store: &campaignTenantStore{campaigns: map[uuid.UUID]storage.Campaign{
			campaignB: {ID: campaignB, TenantID: tenantB},
		}},
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = fetch
	entry := &clientEntry{token: "tok", refs: map[uuid.UUID]struct{}{tenantA: {}, tenantB: {}}, registeredGuilds: map[string]bool{"100": true, "200": true}}
	entry.client.Store(&bot.Client{})
	c.entries["tok"] = entry
	c.tenants[tenantA] = &tenantState{token: "tok", guild: guildA.String()}
	c.tenants[tenantB] = &tenantState{token: "tok", guild: guildB.String()}

	got, err := c.MemberDisplayName(context.Background(), campaignB, user)
	if err != nil {
		t.Fatalf("MemberDisplayName err = %v", err)
	}
	if got != "B-side nick" {
		t.Fatalf("MemberDisplayName = %q, want the owning Tenant's guild nick %q", got, "B-side nick")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fetched) != 1 || fetched[0] != guildB {
		t.Fatalf("fetched guilds = %v, want exactly [%s] (never another Tenant's Guild)", fetched, guildB)
	}
}

func TestMemberDisplayNameFetchErrorPropagates(t *testing.T) {
	sentinel := errors.New("discord 5xx")
	p, campaignID := newNameTestPresence("222222222222222222", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
		return nil, sentinel
	})
	if _, err := p.MemberDisplayName(context.Background(), campaignID, "111111111111111111"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

func TestMemberDisplayNameNoGuild(t *testing.T) {
	p, campaignID := newNameTestPresence("", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
		t.Fatal("fetch must not run without a configured Guild")
		return nil, nil
	})
	if _, err := p.MemberDisplayName(context.Background(), campaignID, "111111111111111111"); !errors.Is(err, ErrNoGuild) {
		t.Fatalf("MemberDisplayName err = %v, want ErrNoGuild in the wait-state (no Guild)", err)
	}
}

// TestMemberDisplayNameUnknownCampaign: a campaign the store cannot resolve
// yields an error (the resolver negative-caches it) and never fetches.
func TestMemberDisplayNameUnknownCampaign(t *testing.T) {
	p, _ := newNameTestPresence("222222222222222222", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
		t.Fatal("fetch must not run for an unresolvable campaign")
		return nil, nil
	})
	if _, err := p.MemberDisplayName(context.Background(), uuid.New(), "111111111111111111"); err == nil {
		t.Fatal("MemberDisplayName err = nil, want error for an unknown campaign")
	}
}
