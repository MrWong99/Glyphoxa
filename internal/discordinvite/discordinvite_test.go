package discordinvite_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/discordinvite"
)

// recordedReq captures the parts of a REST call the resolver's contract pins:
// method + path (so path-escaping is observable) and the auth + UA headers
// Discord requires of a bot.
type recordedReq struct {
	method, path, auth, userAgent string
}

// TestResolve_HappyPath_OnlyVoiceChannelsSortedAndMapped is the golden path: an
// invite resolves to its guild, and the guild's channel list is filtered to
// type-2 GUILD_VOICE only (text=0, category=4, stage=13 dropped), sorted by
// position then name, and mapped to {id,name}. Both calls carry Bot auth + the
// DiscordBot User-Agent, and the code lands path-escaped in the invite URL.
func TestResolve_HappyPath_OnlyVoiceChannelsSortedAndMapped(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		reqs []recordedReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/invites/abc-123":
			_, _ = w.Write([]byte(`{"guild":{"id":"111","name":"The Keep"}}`))
		case "/guilds/111/channels":
			_, _ = w.Write([]byte(`[
				{"id":"1","type":0,"name":"general","position":0},
				{"id":"2","type":2,"name":"Bravo","position":5},
				{"id":"3","type":2,"name":"Alpha","position":5},
				{"id":"4","type":2,"name":"Lobby","position":1},
				{"id":"5","type":4,"name":"Voice Lounge","position":0},
				{"id":"6","type":13,"name":"Stage","position":2}
			]`))
		default:
			http.Error(w, "unexpected path", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := discordinvite.ResolveAt(ctx, "tok-xyz", "abc-123", srv.URL, nil)
	if err != nil {
		t.Fatalf("ResolveAt: %v", err)
	}
	if got.Guild.ID != "111" || got.Guild.Name != "The Keep" {
		t.Errorf("guild = %+v, want {ID:111 Name:The Keep}", got.Guild)
	}

	// Only type-2, sorted position then name: Lobby(pos1), Alpha(pos5), Bravo(pos5).
	want := []discordinvite.VoiceChannel{
		{ID: "4", Name: "Lobby"},
		{ID: "3", Name: "Alpha"},
		{ID: "2", Name: "Bravo"},
	}
	if len(got.VoiceChannels) != len(want) {
		t.Fatalf("voice channels = %+v, want %+v", got.VoiceChannels, want)
	}
	for i := range want {
		if got.VoiceChannels[i] != want[i] {
			t.Errorf("voice[%d] = %+v, want %+v", i, got.VoiceChannels[i], want[i])
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v, want 2 (invite, channels)", reqs)
	}
	for _, rq := range reqs {
		if rq.method != http.MethodGet {
			t.Errorf("method = %q, want GET", rq.method)
		}
		if rq.auth != "Bot tok-xyz" {
			t.Errorf("auth = %q, want 'Bot tok-xyz'", rq.auth)
		}
		if !strings.HasPrefix(rq.userAgent, "DiscordBot ") {
			t.Errorf("user-agent = %q, want a DiscordBot form", rq.userAgent)
		}
	}
	if reqs[0].path != "/invites/abc-123" {
		t.Errorf("invite path = %q, want /invites/abc-123 (path-escaped code)", reqs[0].path)
	}
	if reqs[1].path != "/guilds/111/channels" {
		t.Errorf("channels path = %q, want /guilds/111/channels", reqs[1].path)
	}
}

// TestResolve_InviteNotFound: a 404 on /invites → ErrNotFound (invalid/expired).
func TestResolve_InviteNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Unknown Invite","code":10006}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := discordinvite.ResolveAt(context.Background(), "tok", "gone", srv.URL, nil)
	if !errors.Is(err, discordinvite.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestResolve_GuildlessInvite_NotFound: a group-DM invite resolves 200 but carries
// no guild — it maps to ErrNotFound, same as a missing invite (there is no server
// to pick a voice channel from).
func TestResolve_GuildlessInvite_NotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"guild":null,"channel":{"id":"5","type":3}}`))
	}))
	defer srv.Close()
	_, err := discordinvite.ResolveAt(context.Background(), "tok", "grpdm", srv.URL, nil)
	if !errors.Is(err, discordinvite.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestResolve_ChannelsForbiddenOrNotFound_NoAccess: the invite resolves, but the
// channels read comes back 403 OR 404 — Discord is inconsistent about which it
// returns when the Bot is not a member of the guild, so both map to ErrNoAccess.
func TestResolve_ChannelsForbiddenOrNotFound_NoAccess(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/invites/code" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"guild":{"id":"222","name":"Elsewhere"}}`))
				return
			}
			http.Error(w, "no", status)
		}))
		_, err := discordinvite.ResolveAt(context.Background(), "tok", "code", srv.URL, nil)
		srv.Close()
		if !errors.Is(err, discordinvite.ErrNoAccess) {
			t.Errorf("channels HTTP %d: err = %v, want ErrNoAccess", status, err)
		}
	}
}

// TestResolve_EmptyToken_NoNetwork: an empty token fails fast, before any dial —
// the guard mirrors discordtag, so the default `go test` makes no live call. The
// error is NOT one of the sentinels (they mean specific upstream states).
func TestResolve_EmptyToken_NoNetwork(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()
	_, err := discordinvite.ResolveAt(context.Background(), "", "code", srv.URL, nil)
	if err == nil {
		t.Fatal("empty token returned nil error")
	}
	if errors.Is(err, discordinvite.ErrNotFound) || errors.Is(err, discordinvite.ErrNoAccess) {
		t.Errorf("empty-token error must not be a sentinel: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("empty token made %d HTTP calls, want 0", n)
	}
}

// TestResolve_UpstreamServerError_Generic: a 500 on /invites is a generic wrapped
// error, NOT ErrNotFound/ErrNoAccess — the handler must not translate an outage
// into "invalid invite" or "not a member".
func TestResolve_UpstreamServerError_Generic(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := discordinvite.ResolveAt(context.Background(), "tok", "code", srv.URL, nil)
	if err == nil {
		t.Fatal("upstream 500 returned nil error")
	}
	if errors.Is(err, discordinvite.ErrNotFound) || errors.Is(err, discordinvite.ErrNoAccess) {
		t.Errorf("upstream 500 must be a generic error, not a sentinel: %v", err)
	}
}
