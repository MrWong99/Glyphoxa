package presence

import (
	"errors"
	"testing"
)

const (
	testGuild  = "472093001100"
	otherGuild = "999999999999"
	operatorID = "111111111111"
	strangerID = "222222222222"
)

// fixedGuild builds a KnownGuild predicate that treats a single id as the only
// known Guild (a non-empty id). fixedGuild("") knows no Guild — the wait-state.
func fixedGuild(id string) func(string) bool {
	return func(g string) bool { return id != "" && g == id }
}

// gmList is a scripted GMChecker: the listed snowflakes are GMs.
type gmList map[string]struct{}

func gms(ids ...string) gmList {
	s := gmList{}
	for _, id := range ids {
		if id != "" {
			s[id] = struct{}{}
		}
	}
	return s
}

func (g gmList) IsGM(discordUserID string) bool {
	_, ok := g[discordUserID]
	return ok
}

func TestGateCheckGuild(t *testing.T) {
	g := NewGate(gms(), fixedGuild(testGuild))

	if err := g.CheckGuild(testGuild); err != nil {
		t.Errorf("CheckGuild(configured) = %v, want nil", err)
	}
	if err := g.CheckGuild(otherGuild); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGuild(other) = %v, want ErrWrongGuild", err)
	}
	if err := g.CheckGuild(""); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGuild(DM) = %v, want ErrWrongGuild", err)
	}
}

func TestGateCheckGuildWaitState(t *testing.T) {
	// No configured Guild yet (presence wait-state): deny everything, even a
	// well-formed Guild id.
	g := NewGate(gms(operatorID), fixedGuild(""))
	if err := g.CheckGuild(testGuild); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGuild while unconfigured = %v, want ErrWrongGuild", err)
	}
}

func TestGateCheckGM(t *testing.T) {
	g := NewGate(gms(operatorID), fixedGuild(testGuild))

	if err := g.CheckGM(testGuild, operatorID); err != nil {
		t.Errorf("CheckGM(GM in guild) = %v, want nil", err)
	}
	if err := g.CheckGM(testGuild, strangerID); !errors.Is(err, ErrNotOperator) {
		t.Errorf("CheckGM(stranger in guild) = %v, want ErrNotOperator", err)
	}
	// Wrong Guild fails on the Guild check before the GM check.
	if err := g.CheckGM(otherGuild, operatorID); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGM(GM wrong guild) = %v, want ErrWrongGuild", err)
	}
}
