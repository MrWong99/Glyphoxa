package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

func strptr(s string) *string { return &s }

// newNameTestPresence builds a bare Presence with a configured Guild and an
// injected member fetch, bypassing the standing gateway so MemberDisplayName is
// unit-tested without a live Discord client.
func newNameTestPresence(guildID string, fetch func(ctx context.Context, r rest.Rest, gid, uid snowflake.ID) (*discord.Member, error)) *Presence {
	p := &Presence{}
	p.guildID.Store(guildID)
	p.fetchMember = fetch
	// A non-nil standing client so MemberDisplayName's single client borrow
	// succeeds; its Rest is handed to the injected fetch, which ignores it.
	p.client.Store(&bot.Client{})
	return p
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
