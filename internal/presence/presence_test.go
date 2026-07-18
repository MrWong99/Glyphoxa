package presence

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

type fakePresenceStore struct {
	dep storage.DeploymentConfig
	err error
}

func (f *fakePresenceStore) GetLatestDeploymentConfig(context.Context) (storage.DeploymentConfig, error) {
	return f.dep, f.err
}

// regCall records one commandRegistrar invocation.
type regCall struct {
	guild   string
	defsLen int
	cleared bool // nil defs = clear-old-Guild
}

func mustEnsure(t *testing.T, p *Presence) {
	t.Helper()
	if err := p.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
}

// seededPresence builds a Presence with fake seams and one saved token so a
// single Ensure brings the standing client up. Returns the presence and the
// mutable slices the seams record into.
func seededPresence(t *testing.T) (p *Presence, builds *[]*bot.Client, closed *[]*bot.Client, openErr *error) {
	t.Helper()
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	ct, err := cipher.Seal([]byte("tok-A"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store := &fakePresenceStore{dep: storage.DeploymentConfig{
		GuildID:                   "G1",
		DiscordBotTokenCiphertext: ct,
		DiscordBotTokenLast4:      crypto.Last4("tok-A"),
	}}
	reg := NewRegistry(NewGate(gms(), fixedGuild("")), nil)
	reg.Register(Command{Path: "roll", Description: "Roll dice"})
	p = New(store, cipher, reg, "", nil)

	var bs, cs []*bot.Client
	var oe error
	p.build = func(string) (*bot.Client, error) {
		c := &bot.Client{}
		bs = append(bs, c)
		return c, nil
	}
	p.open = func(context.Context, *bot.Client) error { return oe }
	p.closeClient = func(c *bot.Client) { cs = append(cs, c) }
	p.register = func(context.Context, *bot.Client, string, []discord.ApplicationCommandCreate) error { return nil }
	return p, &bs, &cs, &oe
}

// TestPresenceInvalidatesClientOnGatewayDeath: when the standing client's
// gateway dies (disgo close handler fires, e.g. a mid-session 4004 after the Bot
// token is revoked), the presence drops the corpse — Client returns ErrNoClient
// and the dead client is closed — so the next Voice Session cycle re-runs Ensure
// instead of borrowing the dead client forever (#246).
func TestPresenceInvalidatesClientOnGatewayDeath(t *testing.T) {
	p, builds, closed, _ := seededPresence(t)

	mustEnsure(t, p)
	if len(*builds) != 1 {
		t.Fatalf("builds=%d, want 1", len(*builds))
	}
	dead := (*builds)[0]
	if got, err := p.Client(); err != nil || got != dead {
		t.Fatalf("Client before death = %v/%v, want the built client", got, err)
	}

	// Gateway dies unexpectedly.
	p.invalidate(dead, errors.New("close 4004"))

	if _, err := p.Client(); !errors.Is(err, ErrNoClient) {
		t.Errorf("Client after death = %v, want ErrNoClient", err)
	}
	if len(*closed) != 1 || (*closed)[0] != dead {
		t.Errorf("dead client not closed: closed=%v", *closed)
	}

	// The next borrow re-runs Ensure and rebuilds a fresh standing client.
	cp := p.ClientProvider()
	c, err := cp(context.Background())
	if err != nil {
		t.Fatalf("ClientProvider after death: %v", err)
	}
	if len(*builds) != 2 || c != (*builds)[1] {
		t.Errorf("ClientProvider = %v, want a freshly rebuilt client (builds=%d)", c, len(*builds))
	}
}

// TestPresenceInvalidateIgnoresSupersededClient: a late gateway death from an
// already-replaced client (e.g. the old client after a token-change rebuild)
// must NOT nil the fresh standing client — invalidate compare-and-swaps on
// identity (#246).
func TestPresenceInvalidateIgnoresSupersededClient(t *testing.T) {
	p, builds, closed, _ := seededPresence(t)

	mustEnsure(t, p)
	old := (*builds)[0]

	// Token changes → Ensure closes old and builds a fresh standing client.
	store := p.store.(*fakePresenceStore)
	ct, err := p.cipher.Seal([]byte("tok-B"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store.dep.DiscordBotTokenCiphertext = ct
	store.dep.DiscordBotTokenLast4 = crypto.Last4("tok-B")
	mustEnsure(t, p)
	fresh := (*builds)[1]
	if got, _ := p.Client(); got != fresh {
		t.Fatalf("Client after token change = %v, want fresh", got)
	}

	closesBefore := len(*closed)
	// The superseded old client's gateway dies late — must be a no-op on p.client.
	p.invalidate(old, errors.New("late close 4004"))

	if got, err := p.Client(); err != nil || got != fresh {
		t.Errorf("Client after stale death = %v/%v, want fresh unchanged", got, err)
	}
	if len(*closed) != closesBefore {
		t.Errorf("stale invalidate closed a client: closed=%v", *closed)
	}
}

// TestPresenceRevokedTokenRebuildSurfaces4004: with a revoked token, the
// post-death rebuild's OpenGateway returns a wrapped close 4004; ClientProvider
// surfaces that error to the reconnect loop with the *websocket.CloseError still
// recoverable via errors.As — the exact shape classifyFatal keys on to end the
// session failed with invalid_bot_token instead of retrying forever (#246,
// ADR-0043).
func TestPresenceRevokedTokenRebuildSurfaces4004(t *testing.T) {
	p, builds, _, openErr := seededPresence(t)

	mustEnsure(t, p)
	dead := (*builds)[0]

	// The token is now revoked: any fresh OpenGateway fails with a wrapped 4004.
	*openErr = fmt.Errorf("presence: open gateway: %w", &websocket.CloseError{
		Code: 4004, Text: "Authentication failed",
	})
	p.invalidate(dead, errors.New("gateway died"))

	cp := p.ClientProvider()
	_, err := cp(context.Background())
	if err == nil {
		t.Fatalf("ClientProvider with revoked token: err=nil, want a surfaced error")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != 4004 {
		t.Fatalf("surfaced err = %v, want a recoverable *websocket.CloseError{4004}", err)
	}
}

func TestPresenceEnsureLifecycle(t *testing.T) {
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	seal := func(tok string) (ct []byte, last4 string) {
		c, err := cipher.Seal([]byte(tok))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		return c, crypto.Last4(tok)
	}

	store := &fakePresenceStore{err: storage.ErrNotFound}
	reg := NewRegistry(NewGate(gms(), fixedGuild("")), nil)
	reg.Register(Command{Path: "roll", Description: "Roll dice"})
	wantDefs := len(reg.Definitions())

	p := New(store, cipher, reg, "", nil) // envToken "" so the token comes only from the saved config

	var builds []*bot.Client
	var opens int
	var closed []*bot.Client
	var regs []regCall
	p.build = func(string) (*bot.Client, error) {
		c := &bot.Client{}
		builds = append(builds, c)
		return c, nil
	}
	p.open = func(context.Context, *bot.Client) error { opens++; return nil }
	p.closeClient = func(c *bot.Client) { closed = append(closed, c) }
	p.register = func(_ context.Context, _ *bot.Client, guild string, defs []discord.ApplicationCommandCreate) error {
		regs = append(regs, regCall{guild: guild, defsLen: len(defs), cleared: defs == nil})
		return nil
	}

	// 1. No config → wait-state: no build, no register, nil error.
	mustEnsure(t, p)
	if len(builds) != 0 || len(regs) != 0 {
		t.Fatalf("no-config: builds=%d regs=%d, want 0/0", len(builds), len(regs))
	}
	if _, err := p.Client(); !errors.Is(err, ErrNoClient) {
		t.Errorf("Client in wait-state = %v, want ErrNoClient", err)
	}
	if p.GuildID() != "" {
		t.Errorf("GuildID in wait-state = %q, want empty", p.GuildID())
	}

	// 2. Config but no token (nothing saved, empty envToken) → still wait-state.
	store.err = nil
	store.dep = storage.DeploymentConfig{GuildID: "G1"}
	mustEnsure(t, p)
	if len(builds) != 0 {
		t.Fatalf("no-token: builds=%d, want 0", len(builds))
	}

	// 3. A saved token appears → one build + open + one full register of G1.
	ctA, l4A := seal("tok-A")
	store.dep = storage.DeploymentConfig{GuildID: "G1", DiscordBotTokenCiphertext: ctA, DiscordBotTokenLast4: l4A}
	mustEnsure(t, p)
	if len(builds) != 1 || opens != 1 {
		t.Fatalf("first token: builds=%d opens=%d, want 1/1", len(builds), opens)
	}
	if len(regs) != 1 || regs[0].guild != "G1" || regs[0].cleared || regs[0].defsLen != wantDefs {
		t.Fatalf("first register = %+v, want one full G1 with %d defs", regs, wantDefs)
	}
	if got, err := p.Client(); err != nil || got != builds[0] {
		t.Errorf("Client = %v/%v, want the built client", got, err)
	}
	if p.GuildID() != "G1" {
		t.Errorf("GuildID = %q, want G1", p.GuildID())
	}

	// 4. Idempotent repeat: same token + Guild → zero new calls.
	mustEnsure(t, p)
	if len(builds) != 1 || opens != 1 || len(regs) != 1 {
		t.Fatalf("idempotent repeat: builds=%d opens=%d regs=%d, want 1/1/1", len(builds), opens, len(regs))
	}

	// 5. Guild change only → re-register, no rebuild, clear the old Guild.
	store.dep.GuildID = "G2"
	mustEnsure(t, p)
	if len(builds) != 1 {
		t.Fatalf("guild change rebuilt the client: builds=%d, want 1", len(builds))
	}
	if len(regs) != 3 {
		t.Fatalf("guild change regs=%d, want 3 (clear G1 + full G2)", len(regs))
	}
	if !regs[1].cleared || regs[1].guild != "G1" {
		t.Errorf("regs[1] = %+v, want a clear of old Guild G1", regs[1])
	}
	if regs[2].cleared || regs[2].guild != "G2" || regs[2].defsLen != wantDefs {
		t.Errorf("regs[2] = %+v, want a full register of G2", regs[2])
	}

	// 6. Token change → close old client + rebuild + register.
	ctB, l4B := seal("tok-B")
	store.dep = storage.DeploymentConfig{GuildID: "G2", DiscordBotTokenCiphertext: ctB, DiscordBotTokenLast4: l4B}
	mustEnsure(t, p)
	if len(builds) != 2 {
		t.Fatalf("token change builds=%d, want 2", len(builds))
	}
	if len(closed) != 1 || closed[0] != builds[0] {
		t.Fatalf("token change closed=%v, want the old client closed exactly once", closed)
	}
	if len(regs) != 4 || regs[3].guild != "G2" || regs[3].cleared {
		t.Errorf("regs[3] = %+v, want a full re-register of G2 after rebuild", regs)
	}

	// ClientProvider hands out the current standing client.
	cp := p.ClientProvider()
	if c, err := cp(context.Background()); err != nil || c != builds[1] {
		t.Errorf("ClientProvider = %v/%v, want the rebuilt client", c, err)
	}

	// Close tears the client down and returns to the wait-state.
	p.Close()
	if len(closed) != 2 || closed[1] != builds[1] {
		t.Errorf("Close did not close the standing client: closed=%v", closed)
	}
	if _, err := p.Client(); !errors.Is(err, ErrNoClient) {
		t.Errorf("Client after Close = %v, want ErrNoClient", err)
	}
}
