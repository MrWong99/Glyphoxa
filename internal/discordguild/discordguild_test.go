package discordguild_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/discordguild"
)

// fakeGuildAPI is a scripted Discord REST fake for the two CheckAdmin reads:
// GET /guilds/{id} and GET /guilds/{id}/members/{userID}. It counts requests so
// tests can assert short-circuits (owner never fetches the member).
type fakeGuildAPI struct {
	guildStatus  int
	guild        map[string]any
	memberStatus int
	member       map[string]any

	guildCalls  atomic.Int64
	memberCalls atomic.Int64
}

func (f *fakeGuildAPI) server(t *testing.T, guildID, userID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/guilds/"+guildID+"/members/"+userID, func(w http.ResponseWriter, _ *http.Request) {
		f.memberCalls.Add(1)
		w.WriteHeader(f.memberStatus)
		if f.memberStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(f.member)
		}
	})
	mux.HandleFunc("/guilds/"+guildID, func(w http.ResponseWriter, _ *http.Request) {
		f.guildCalls.Add(1)
		w.WriteHeader(f.guildStatus)
		if f.guildStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(f.guild)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const (
	testGuildID = "472093001100000000"
	testUserID  = "555000000000000000"
)

// TestCheckAdmin_OwnerShortCircuits: the guild owner passes on the guild read
// alone — the member endpoint is never called (owners may hold no explicit role).
func TestCheckAdmin_OwnerShortCircuits(t *testing.T) {
	t.Parallel()
	fake := &fakeGuildAPI{
		guildStatus: http.StatusOK,
		guild: map[string]any{
			"id": testGuildID, "owner_id": testUserID,
			"roles": []map[string]any{},
		},
		memberStatus: http.StatusOK,
	}
	srv := fake.server(t, testGuildID, testUserID)

	err := discordguild.CheckAdminAt(context.Background(), "tok", testGuildID, testUserID, srv.URL, nil)
	if err != nil {
		t.Fatalf("CheckAdmin(owner) = %v, want nil", err)
	}
	if got := fake.memberCalls.Load(); got != 0 {
		t.Errorf("member endpoint called %d times for the owner, want 0", got)
	}
}

// TestCheckAdmin_RoleBits: a member passes when any held role — the implicit
// @everyone role (id == guild id) included — carries MANAGE_GUILD (0x20) or
// ADMINISTRATOR (0x8); without either bit the proof fails with ErrNoPermission.
// The permissions field is a decimal STRING per the Discord API.
func TestCheckAdmin_RoleBits(t *testing.T) {
	t.Parallel()

	roleID := "600000000000000001"
	for name, tc := range map[string]struct {
		roles       []map[string]any // guild role table
		memberRoles []string
		wantErr     error
	}{
		"manage guild bit": {
			roles:       []map[string]any{{"id": roleID, "permissions": "32"}},
			memberRoles: []string{roleID},
		},
		"administrator bit": {
			roles:       []map[string]any{{"id": roleID, "permissions": "8"}},
			memberRoles: []string{roleID},
		},
		"everyone role grants": {
			// @everyone's id equals the guild id and is held implicitly: empty
			// member roles still pass.
			roles:       []map[string]any{{"id": testGuildID, "permissions": "32"}},
			memberRoles: []string{},
		},
		"no bits": {
			roles: []map[string]any{
				{"id": testGuildID, "permissions": "1049600"}, // speak/view, no manage
				{"id": roleID, "permissions": "3146752"},
			},
			memberRoles: []string{roleID},
			wantErr:     discordguild.ErrNoPermission,
		},
		"unheld admin role does not grant": {
			roles:       []map[string]any{{"id": roleID, "permissions": "8"}},
			memberRoles: []string{},
			wantErr:     discordguild.ErrNoPermission,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeGuildAPI{
				guildStatus: http.StatusOK,
				guild: map[string]any{
					"id": testGuildID, "owner_id": "othr0000000000000000",
					"roles": tc.roles,
				},
				memberStatus: http.StatusOK,
				member:       map[string]any{"roles": tc.memberRoles},
			}
			srv := fake.server(t, testGuildID, testUserID)
			err := discordguild.CheckAdminAt(context.Background(), "tok", testGuildID, testUserID, srv.URL, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("CheckAdmin = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestCheckAdmin_GuildReadDenied: the guild read failing 403 OR 404 means the
// Bot is not a member of that guild (Discord is inconsistent about which code) —
// both collapse to ErrBotNotInGuild, and the member endpoint is never reached.
func TestCheckAdmin_GuildReadDenied(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		fake := &fakeGuildAPI{guildStatus: status, memberStatus: http.StatusOK}
		srv := fake.server(t, testGuildID, testUserID)
		err := discordguild.CheckAdminAt(context.Background(), "tok", testGuildID, testUserID, srv.URL, nil)
		if !errors.Is(err, discordguild.ErrBotNotInGuild) {
			t.Errorf("guild read %d: err = %v, want ErrBotNotInGuild", status, err)
		}
		if got := fake.memberCalls.Load(); got != 0 {
			t.Errorf("guild read %d: member endpoint called %d times, want 0", status, got)
		}
	}
}

// TestCheckAdmin_MemberRead: member 404 = the user is not in the (resolvable)
// guild → ErrUserNotInGuild; member 403 = the Bot lost access → ErrBotNotInGuild.
func TestCheckAdmin_MemberRead(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]error{
		http.StatusNotFound:  discordguild.ErrUserNotInGuild,
		http.StatusForbidden: discordguild.ErrBotNotInGuild,
	} {
		fake := &fakeGuildAPI{
			guildStatus: http.StatusOK,
			guild: map[string]any{
				"id": testGuildID, "owner_id": "othr0000000000000000",
				"roles": []map[string]any{},
			},
			memberStatus: status,
		}
		srv := fake.server(t, testGuildID, testUserID)
		err := discordguild.CheckAdminAt(context.Background(), "tok", testGuildID, testUserID, srv.URL, nil)
		if !errors.Is(err, want) {
			t.Errorf("member read %d: err = %v, want %v", status, err, want)
		}
	}
}

// TestCheckAdmin_EmptyTokenFailsFast: no token, no dial — zero HTTP requests.
func TestCheckAdmin_EmptyTokenFailsFast(t *testing.T) {
	t.Parallel()
	fake := &fakeGuildAPI{guildStatus: http.StatusOK, memberStatus: http.StatusOK}
	srv := fake.server(t, testGuildID, testUserID)

	err := discordguild.CheckAdminAt(context.Background(), "", testGuildID, testUserID, srv.URL, nil)
	if err == nil {
		t.Fatal("CheckAdmin with empty token = nil, want error")
	}
	if got := fake.guildCalls.Load() + fake.memberCalls.Load(); got != 0 {
		t.Errorf("empty token dialed %d times, want 0", got)
	}
}

// TestCheckAdmin_UnexpectedStatusIsGenericError: a 500 from either read is a
// plain error, never one of the sentinels (the RPC layer maps it to Internal).
func TestCheckAdmin_UnexpectedStatusIsGenericError(t *testing.T) {
	t.Parallel()
	for name, fake := range map[string]*fakeGuildAPI{
		"guild 500": {guildStatus: http.StatusInternalServerError, memberStatus: http.StatusOK},
		"member 500": {
			guildStatus: http.StatusOK,
			guild: map[string]any{
				"id": testGuildID, "owner_id": "othr0000000000000000",
				"roles": []map[string]any{},
			},
			memberStatus: http.StatusInternalServerError,
		},
	} {
		srv := fake.server(t, testGuildID, testUserID)
		err := discordguild.CheckAdminAt(context.Background(), "tok", testGuildID, testUserID, srv.URL, nil)
		if err == nil {
			t.Errorf("%s: err = nil, want generic error", name)
			continue
		}
		for _, sentinel := range []error{
			discordguild.ErrBotNotInGuild, discordguild.ErrUserNotInGuild, discordguild.ErrNoPermission,
		} {
			if errors.Is(err, sentinel) {
				t.Errorf("%s: err = %v, must not be sentinel %v", name, err, sentinel)
			}
		}
	}
}
