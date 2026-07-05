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

func savedTokenDep(t *testing.T, cipher *crypto.Cipher, guild, tok string) storage.DeploymentConfig {
	t.Helper()
	ct, err := cipher.Seal([]byte(tok))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return storage.DeploymentConfig{GuildID: guild, DiscordBotTokenCiphertext: ct, DiscordBotTokenLast4: crypto.Last4(tok)}
}

// TestClientProviderReEnsuresAfterFailure pins issue #2: a boot-time Discord blip
// leaves the presence in the wait-state, but the next Voice Session cycle's
// ClientProvider call re-runs Ensure and self-heals — the presence is not stuck
// dead (and every Voice Session silently failing) until a restart.
func TestClientProviderReEnsuresAfterFailure(t *testing.T) {
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store := &fakePresenceStore{dep: savedTokenDep(t, cipher, "G1", "tok")}
	reg := NewRegistry(NewGate(auth.ParseOperatorAllowlist(""), fixedGuild("")), nil)
	reg.Register(Command{Path: "roll", Description: "Roll dice"})
	p := New(store, cipher, reg, "", nil)

	opens := 0
	p.open = func(context.Context, *bot.Client) error {
		opens++
		if opens == 1 {
			return errors.New("discord blip at boot")
		}
		return nil
	}
	p.build = func(string) (*bot.Client, error) { return &bot.Client{}, nil }
	p.register = func(context.Context, *bot.Client, string, []discord.ApplicationCommandCreate) error { return nil }
	p.closeClient = func(*bot.Client) {}

	// Boot Ensure fails on the blip → wait-state.
	if err := p.Ensure(context.Background()); err == nil {
		t.Fatal("first Ensure = nil, want the open error")
	}
	if _, err := p.Client(); !errors.Is(err, ErrNoClient) {
		t.Fatalf("after failed Ensure = %v, want ErrNoClient (wait-state)", err)
	}

	// The next Voice Session cycle's ClientProvider re-runs Ensure → success.
	c, err := p.ClientProvider()(context.Background())
	if err != nil {
		t.Fatalf("ClientProvider after failure: %v", err)
	}
	if c == nil {
		t.Fatal("ClientProvider returned a nil client after re-Ensure")
	}
	if opens != 2 {
		t.Errorf("open calls = %d, want 2 (fail at boot, succeed via provider re-Ensure)", opens)
	}
}

// TestEnsureClearsCommandsWhenGuildRemoved pins issue #5: clearing the Guild
// (G1 → "") while a token is still configured must remove the stale commands
// from the old Guild, not silently skip the register block.
func TestEnsureClearsCommandsWhenGuildRemoved(t *testing.T) {
	cipher, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store := &fakePresenceStore{dep: savedTokenDep(t, cipher, "G1", "tok")}
	reg := NewRegistry(NewGate(auth.ParseOperatorAllowlist(""), fixedGuild("")), nil)
	reg.Register(Command{Path: "roll", Description: "Roll dice"})
	p := New(store, cipher, reg, "", nil)

	var regs []regCall
	p.build = func(string) (*bot.Client, error) { return &bot.Client{}, nil }
	p.open = func(context.Context, *bot.Client) error { return nil }
	p.closeClient = func(*bot.Client) {}
	p.register = func(_ context.Context, _ *bot.Client, guild string, defs []discord.ApplicationCommandCreate) error {
		regs = append(regs, regCall{guild: guild, defsLen: len(defs), cleared: defs == nil})
		return nil
	}

	mustEnsure(t, p)
	if len(regs) != 1 || regs[0].guild != "G1" || regs[0].cleared {
		t.Fatalf("initial regs = %+v, want one full G1", regs)
	}

	// Guild removed, token still present.
	store.dep.GuildID = ""
	mustEnsure(t, p)
	if p.GuildID() != "" {
		t.Errorf("GuildID after clear = %q, want empty", p.GuildID())
	}
	if len(regs) != 2 || !regs[1].cleared || regs[1].guild != "G1" {
		t.Errorf("clear regs = %+v, want a clear (nil defs) of the removed G1", regs)
	}
}
