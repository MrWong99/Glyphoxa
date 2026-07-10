package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// newMembersTestPresence builds a bare Presence with a real (in-memory) disgo
// cache — populated directly via Put/SetSelfUser, no gateway involved — and an
// injected member fetch, so VoiceChannelMembers is unit-tested without a live
// Discord client. This reuses the same fetchMember seam as MemberDisplayName
// (see newNameTestPresence in members_name_test.go): VoiceChannelMembers' one
// disgo-specific dependency the existing seam doesn't cover is the voice-state
// cache and SelfUser, and the real in-memory cache impl exercises those exactly
// as production does, so no second injected seam is needed for them.
func newMembersTestPresence(fetch func(ctx context.Context, guildID, userID snowflake.ID) (*discord.Member, error)) *Presence {
	p := &Presence{}
	p.fetchMember = fetch
	p.client.Store(&bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll))})
	return p
}

func putVoiceState(p *Presence, guildID, channelID, userID snowflake.ID) {
	p.client.Load().Caches.VoiceStateCache().Put(guildID, userID, discord.VoiceState{
		GuildID:   guildID,
		ChannelID: &channelID,
		UserID:    userID,
	})
}

func TestVoiceChannelMembers_SkipsMemberOnGetMemberError(t *testing.T) {
	const guildID snowflake.ID = 100
	const channelID snowflake.ID = 200
	const okUser snowflake.ID = 1
	const badUser snowflake.ID = 2

	sentinel := errors.New("rate limited")
	p := newMembersTestPresence(func(_ context.Context, _, userID snowflake.ID) (*discord.Member, error) {
		if userID == badUser {
			return nil, sentinel
		}
		return &discord.Member{User: discord.User{ID: userID, Username: "ok-user"}}, nil
	})
	putVoiceState(p, guildID, channelID, okUser)
	putVoiceState(p, guildID, channelID, badUser)

	members, err := p.VoiceChannelMembers(context.Background(), channelID)
	if err != nil {
		t.Fatalf("VoiceChannelMembers err = %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("VoiceChannelMembers len = %d, want 1 (bad member skipped, not whole list blanked)", len(members))
	}
	if members[0].ID != okUser {
		t.Fatalf("VoiceChannelMembers[0].ID = %v, want %v", members[0].ID, okUser)
	}
}

func TestVoiceChannelMembers_FiltersSelfWhenKnown(t *testing.T) {
	const guildID snowflake.ID = 100
	const channelID snowflake.ID = 200
	const botUser snowflake.ID = 1
	const otherUser snowflake.ID = 2

	p := newMembersTestPresence(func(_ context.Context, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	p.client.Load().Caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: botUser}})
	putVoiceState(p, guildID, channelID, botUser)
	putVoiceState(p, guildID, channelID, otherUser)

	members, err := p.VoiceChannelMembers(context.Background(), channelID)
	if err != nil {
		t.Fatalf("VoiceChannelMembers err = %v", err)
	}
	if len(members) != 1 || members[0].ID != otherUser {
		t.Fatalf("VoiceChannelMembers = %+v, want only otherUser (bot self-filtered)", members)
	}
}

func TestVoiceChannelMembers_BotAppearsWhenSelfUnknown(t *testing.T) {
	const guildID snowflake.ID = 100
	const channelID snowflake.ID = 200
	const botUser snowflake.ID = 1

	p := newMembersTestPresence(func(_ context.Context, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	// SelfUser deliberately left unset (unknown): a zero-value self id must not
	// masquerade as a real user and accidentally filter someone out.
	putVoiceState(p, guildID, channelID, botUser)

	members, err := p.VoiceChannelMembers(context.Background(), channelID)
	if err != nil {
		t.Fatalf("VoiceChannelMembers err = %v", err)
	}
	if len(members) != 1 || members[0].ID != botUser {
		t.Fatalf("VoiceChannelMembers = %+v, want bot to appear when SelfUser is unknown", members)
	}
}
