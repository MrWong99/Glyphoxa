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

	"github.com/MrWong99/Glyphoxa/internal/storage"
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
	store  *memberOwnerStore
}

// memberOwnerStore is the minimal TenantStore the member-picker tests need: it
// answers GetTenantIDByGuildID (the Guild→owning-Tenant authority the picker checks,
// #490). The deployment-config reads are unused on this path.
type memberOwnerStore struct {
	owner map[string]uuid.UUID // guild_id -> newest-wins owning Tenant
}

func (s *memberOwnerStore) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, storage.ErrNotFound
}
func (s *memberOwnerStore) ListDeploymentConfigs(context.Context) ([]storage.DeploymentConfig, error) {
	return nil, nil
}
func (s *memberOwnerStore) GetTenantIDByGuildID(_ context.Context, guildID string) (uuid.UUID, error) {
	if id, ok := s.owner[guildID]; ok {
		return id, nil
	}
	return uuid.Nil, storage.ErrNotFound
}

func newMembersTestPresence(fetch func(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error)) *membersRig {
	tid := uuid.New()
	store := &memberOwnerStore{owner: map[string]uuid.UUID{"100": tid}}
	c := &Clients{
		store:   store,
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = fetch
	cl := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll))}
	entry := &clientEntry{token: "tok", refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
	entry.client.Store(cl)
	entry.refs[tid] = struct{}{}
	c.entries["tok"] = entry
	c.tenants[tid] = &tenantState{token: "tok", guild: "100"}
	return &membersRig{c: c, tenant: tid, entry: entry, client: cl, store: store}
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

// TestVoiceChannelMembers_ScopedToTenantGuild pins finding 2: a central-token
// client's voice-state cache holds occupants of every Tenant's Guild that shares
// the token. VoiceChannelMembers must scope to the CALLING Tenant's own Guild, so
// Tenant A configuring Tenant B's channel snowflake never lists B's occupants.
func TestVoiceChannelMembers_ScopedToTenantGuild(t *testing.T) {
	const guildA snowflake.ID = 100
	const guildB snowflake.ID = 200
	const channel snowflake.ID = 999
	const userInB snowflake.ID = 7

	a, b := uuid.New(), uuid.New()
	c := &Clients{
		store:   &memberOwnerStore{owner: map[string]uuid.UUID{guildA.String(): a, guildB.String(): b}},
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	}
	cl := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll))}
	entry := &clientEntry{token: "central", refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
	entry.client.Store(cl)
	entry.refs[a] = struct{}{}
	entry.refs[b] = struct{}{}
	c.entries["central"] = entry
	c.tenants[a] = &tenantState{token: "central", guild: guildA.String()}
	c.tenants[b] = &tenantState{token: "central", guild: guildB.String()}

	// A user sits in Tenant B's Guild, in a channel snowflake Tenant A also names.
	putVoiceState(cl, guildB, channel, userInB)

	// Tenant A lists that channel: scoped to Guild A → B's occupant is invisible.
	membersA, err := c.VoiceChannelMembers(context.Background(), a, channel)
	if err != nil {
		t.Fatalf("A members: %v", err)
	}
	if len(membersA) != 0 {
		t.Fatalf("Tenant A saw %d members in another Tenant's Guild (cross-tenant leak), want 0", len(membersA))
	}

	// Tenant B lists the same channel: its own Guild → the occupant appears.
	membersB, err := c.VoiceChannelMembers(context.Background(), b, channel)
	if err != nil {
		t.Fatalf("B members: %v", err)
	}
	if len(membersB) != 1 || membersB[0].ID != userInB {
		t.Fatalf("Tenant B members = %+v, want its own Guild's occupant", membersB)
	}
}

// TestVoiceChannelMembers_TwoTenantsSameGuildLoserRejected pins the guild-ownership
// agreement (#490 review item a): two Tenants configure the SAME guild_id, and the
// interaction router resolves it to the NEWEST-updated owner. The member picker must
// agree — the winner reads its channel, the stale LOSING row is cleanly rejected
// (empty list), so it can never see the winner's voice-channel occupants even though
// its own tenantState still names that Guild.
func TestVoiceChannelMembers_TwoTenantsSameGuildLoserRejected(t *testing.T) {
	const guild snowflake.ID = 100
	const channel snowflake.ID = 999
	const occupant snowflake.ID = 7

	winner, loser := uuid.New(), uuid.New()
	c := &Clients{
		// The store resolves the shared Guild to the winner (newest-wins authority).
		store:   &memberOwnerStore{owner: map[string]uuid.UUID{guild.String(): winner}},
		entries: map[string]*clientEntry{},
		tenants: map[uuid.UUID]*tenantState{},
	}
	c.fetchMember = func(_ context.Context, _ rest.Rest, _, userID snowflake.ID) (*discord.Member, error) {
		return &discord.Member{User: discord.User{ID: userID, Username: "u"}}, nil
	}
	cl := &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagsAll))}
	entry := &clientEntry{token: "central", refs: map[uuid.UUID]struct{}{}, registeredGuilds: map[string]bool{}}
	entry.client.Store(cl)
	entry.refs[winner] = struct{}{}
	entry.refs[loser] = struct{}{}
	c.entries["central"] = entry
	// BOTH Tenants' state still names the same Guild — the loser's stale row.
	c.tenants[winner] = &tenantState{token: "central", guild: guild.String()}
	c.tenants[loser] = &tenantState{token: "central", guild: guild.String()}

	putVoiceState(cl, guild, channel, occupant)

	// The winner (newest-wins owner) reads its channel's occupant.
	membersWin, err := c.VoiceChannelMembers(context.Background(), winner, channel)
	if err != nil {
		t.Fatalf("winner members: %v", err)
	}
	if len(membersWin) != 1 || membersWin[0].ID != occupant {
		t.Fatalf("winner members = %+v, want the occupant", membersWin)
	}

	// The loser is cleanly rejected — no view into the winner's channel.
	membersLose, err := c.VoiceChannelMembers(context.Background(), loser, channel)
	if err != nil {
		t.Fatalf("loser members: %v", err)
	}
	if len(membersLose) != 0 {
		t.Fatalf("stale losing Tenant saw %d members of the shared Guild (cross-tenant leak), want 0", len(membersLose))
	}
}
