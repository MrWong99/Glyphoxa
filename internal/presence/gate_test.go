package presence

import (
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

const (
	testGuild  = "472093001100"
	otherGuild = "999999999999"
	operatorID = "111111111111"
	strangerID = "222222222222"
)

func fixedGuild(id string) func() string { return func() string { return id } }

func TestGateCheckGuild(t *testing.T) {
	g := NewGate(auth.ParseOperatorAllowlist(""), fixedGuild(testGuild))

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
	g := NewGate(auth.ParseOperatorAllowlist(operatorID), fixedGuild(""))
	if err := g.CheckGuild(testGuild); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGuild while unconfigured = %v, want ErrWrongGuild", err)
	}
}

func TestGateCheckGM(t *testing.T) {
	g := NewGate(auth.ParseOperatorAllowlist(operatorID), fixedGuild(testGuild))

	if err := g.CheckGM(testGuild, operatorID); err != nil {
		t.Errorf("CheckGM(operator in guild) = %v, want nil", err)
	}
	if err := g.CheckGM(testGuild, strangerID); !errors.Is(err, ErrNotOperator) {
		t.Errorf("CheckGM(stranger in guild) = %v, want ErrNotOperator", err)
	}
	// Wrong Guild fails on the Guild check before the operator check.
	if err := g.CheckGM(otherGuild, operatorID); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("CheckGM(operator wrong guild) = %v, want ErrWrongGuild", err)
	}
}
