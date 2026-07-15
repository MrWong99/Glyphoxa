//go:build dave

package voice

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	davesession "github.com/thomas-vilte/dave-go/session"
)

// DaveOption returns the [bot.ConfigOpt] that wires DAVE/MLS end-to-end voice
// encryption into the client. Pass it to disgo.New alongside the gateway opts;
// [NewManager] then borrows the DAVE-capable voice manager.
//
// DAVE is mandatory in production (ADR-0006: Discord close code 4017 without
// it). The implementation is thomas-vilte/dave-go — pure Go (DAVE v1 over the
// author's RFC 9420 mls-go), no CGO, no libdave shared library (which this
// build required before the ADR-0006 amendment). The `dave` tag no longer
// implies a native toolchain; it still selects real encryption over the stub
// (dave_stub.go) whose DaveOption is a no-op and [DaveAvailable] false, so
// tests and tooling keep building without pulling in the MLS stack.
func DaveOption() bot.ConfigOpt {
	return bot.WithVoiceManagerConfigOpts(
		voice.WithDaveSessionCreateFunc(davesession.CreateFunc()),
	)
}

// DaveAvailable reports whether this build can wire real DAVE encryption. It is
// true in the DAVE build (this file) and false in the default stub build.
func DaveAvailable() bool { return true }
