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
	"github.com/google/uuid"
)

// membersRig is a bare Clients registry with one standing client (a real
// in-memory disgo cache, populated directly via Put/SetSelfUser — no gateway) and
// an injected member fetch, so VoiceChannelMembers is unit-tested without a live
// Discord client. entry.client is exposed so a test can nil it mid-loop.
type membersRig struct {
	c      *Clients
	tenant uuid.UUID
	entry  *clientEntry
	client *bot.Client
}

func newMembersTestPresence(fetch func(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error)) *membersRig {
	c := &Clients{
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = fetch
	cl := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll))}
	entry := &clientEntry{token: "tok", refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
	entry.client.Store(cl)
	tid := uuid.New()
	entry.refs[tid] = struct{}{}
	c.entries["tok"] = entry
	c.tenants[tid] = &tenantState{token: "tok", guild: "100"}
	return &membersRig{c: c, tenant: tid, entry: entry, client: cl}
}

func (r *membersRig) members(ctx context.Context, channelID snowflake.ID) ([]Member, error) {
	return r.c.VoiceChannelMembers(ctx, r.tenant, channelID)
}

func putVoiceState(cl *bot.Client, guildID, channelID, userID snowflake.ID) {
	cl.Caches.VoiceStateCache().Put(guildID, userID, discord.VoiceState{
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
	rig := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		if userID == badUser {
			return nil, sentinel
		}
		return &discord.Member{User: discord.User{ID: userID, Username: "ok-user"}}, nil
	})
	putVoiceState(rig.client, guildID, channelID, okUser)
	putVoiceState(rig.client, guildID, channelID, badUser)

	members, err := rig.members(context.Background(), channelID)
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

	var rig *membersRig
	// Simulate a concurrent Close/token-rebuild landing between the first and
	// second member fetch: the standing client pointer goes nil mid-loop. Because
	// VoiceChannelMembers borrowed the rest.Rest once up front and hands that same
	// snapshot to every fetch, both members must still resolve — a re-borrow would
	// return ErrNoClient for userB and blank it from the picker.
	fetched := 0
	rig = newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		fetched++
		if fetched == 1 {
			rig.entry.client.Store(nil) // wait-state now; a per-member re-borrow would fail here
		}
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	putVoiceState(rig.client, guildID, channelID, userA)
	putVoiceState(rig.client, guildID, channelID, userB)

	members, err := rig.members(context.Background(), channelID)
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

	rig := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	rig.client.Caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: botUser}})
	putVoiceState(rig.client, guildID, channelID, botUser)
	putVoiceState(rig.client, guildID, channelID, otherUser)

	members, err := rig.members(context.Background(), channelID)
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

	rig := newMembersTestPresence(func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	})
	// SelfUser deliberately left unset (unknown). Include an occupant whose id is
	// the ZERO snowflake: this pins the members.go self-filter guard exactly. If the
	// self-filter fired on an unknown SelfUser, the cached zero-value self.ID (0)
	// would masquerade as this occupant and wrongly drop it. It must appear.
	const zeroUser snowflake.ID = 0
	putVoiceState(rig.client, guildID, channelID, botUser)
	putVoiceState(rig.client, guildID, channelID, zeroUser)

	members, err := rig.members(context.Background(), channelID)
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
