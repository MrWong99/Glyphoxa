package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

func strptr(s string) *string { return &s }

// newNameTestPresence builds a bare Clients registry with one standing client
// whose entry registers guildID (empty = no known Guild, the wait-state), plus an
// injected member fetch, so MemberDisplayName is unit-tested without a live
// gateway. Its Rest is handed to the injected fetch, which ignores it.
func newNameTestPresence(guildID string, fetch func(ctx context.Context, r rest.Rest, gid, uid snowflake.ID) (*discord.Member, error)) *Clients {
	c := &Clients{
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = fetch
	entry := &clientEntry{token: "tok", refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
	entry.client.Store(&bot.Client{})
	if guildID != "" {
		entry.registeredGuilds[guildID] = true
	}
	c.entries["tok"] = entry
	return c
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
			p := newNameTestPresence("222222222222222222", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
				return tc.member, nil
			})
			got, err := p.MemberDisplayName(context.Background(), user)
			if err != nil {
				t.Fatalf("MemberDisplayName err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("MemberDisplayName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMemberDisplayNameFetchErrorPropagates(t *testing.T) {
	sentinel := errors.New("discord 5xx")
	p := newNameTestPresence("222222222222222222", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
		return nil, sentinel
	})
	if _, err := p.MemberDisplayName(context.Background(), "111111111111111111"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

func TestMemberDisplayNameNoGuild(t *testing.T) {
	p := newNameTestPresence("", func(_ context.Context, _ rest.Rest, _, _ snowflake.ID) (*discord.Member, error) {
		t.Fatal("fetch must not run without a configured Guild")
		return nil, nil
	})
	if _, err := p.MemberDisplayName(context.Background(), "111111111111111111"); err == nil {
		t.Fatal("MemberDisplayName err = nil, want error in the wait-state (no Guild)")
	}
}
