package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
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
func newMembersTestPresence(fetch func(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error)) *Presence {
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
	p := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
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

func TestVoiceChannelMembers_ClientClearedMidLoopStillServesSnapshot(t *testing.T) {
	const guildID snowflake.ID = 100
	const channelID snowflake.ID = 200
	const userA snowflake.ID = 1
	const userB snowflake.ID = 2

	var p *Presence
	// Simulate a concurrent Close/token-rebuild landing between the first and
	// second member fetch: the standing client pointer goes nil mid-loop. Because
	// VoiceChannelMembers borrowed the rest.Rest once up front and hands that same
	// snapshot to every fetch, both members must still resolve — a re-borrow would
	// return ErrNoClient for userB and blank it from the picker.
	fetched := 0
	p = newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		fetched++
		if fetched == 1 {
			p.client.Store(nil) // wait-state now; a per-member p.Client() would fail/nil-panic here on
		}
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	putVoiceState(p, guildID, channelID, userA)
	putVoiceState(p, guildID, channelID, userB)

	members, err := p.VoiceChannelMembers(context.Background(), channelID)
	if err != nil {
		t.Fatalf("VoiceChannelMembers err = %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("VoiceChannelMembers len = %d, want 2 (snapshot survives a mid-loop client clear)", len(members))
	}
}

func TestVoiceChannelMembers_FiltersSelfWhenKnown(t *testing.T) {
	const guildID snowflake.ID = 100
	const channelID snowflake.ID = 200
	const botUser snowflake.ID = 1
	const otherUser snowflake.ID = 2

	p := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
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

	p := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	// SelfUser deliberately left unset (unknown). Include an occupant whose id is
	// the ZERO snowflake: this pins the members.go:47-49 guard exactly. If the
	// self-filter fired on an unknown SelfUser, the cached zero-value self.ID (0)
	// would masquerade as this occupant and wrongly drop it. It must appear.
	const zeroUser snowflake.ID = 0
	putVoiceState(p, guildID, channelID, botUser)
	putVoiceState(p, guildID, channelID, zeroUser)

	members, err := p.VoiceChannelMembers(context.Background(), channelID)
	if err != nil {
		t.Fatalf("VoiceChannelMembers err = %v", err)
	}
	got := map[snowflake.ID]bool{}
	for _, m := range members {
		got[m.ID] = true
	}
	if !got[botUser] || !got[zeroUser] || len(members) != 2 {
		t.Fatalf("VoiceChannelMembers = %+v, want both botUser and the zero-id occupant to appear when SelfUser is unknown", members)
	}
}
