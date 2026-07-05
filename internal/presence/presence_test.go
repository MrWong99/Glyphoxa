package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/Glyphoxa/internal/auth"
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
	reg := NewRegistry(NewGate(auth.ParseOperatorAllowlist(""), fixedGuild("")), nil)
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
