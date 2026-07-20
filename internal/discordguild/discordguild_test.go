package discordguild_test

import (
	"context"
	"encoding/json"
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
