//go:build dave

package voice

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
)

// DaveOption returns the [bot.ConfigOpt] that wires DAVE/MLS end-to-end voice
// encryption into the client. Pass it to disgo.New alongside the gateway opts;
// [NewManager] then borrows the DAVE-capable voice manager.
//
// DAVE is mandatory in production (ADR-0006: Discord close code 4017 without
// it). This is the DAVE build (`-tags dave`): it links libdave through
// godave/golibdave via CGO, so the libdave shared library must be installed
// (see `make dave-libs`). The default build (no `dave` tag) compiles a stub
// whose DaveOption is a no-op and [DaveAvailable] is false, keeping the package
// and its race tests buildable without the C library.
func DaveOption() bot.ConfigOpt {
	return bot.WithVoiceManagerConfigOpts(
		voice.WithDaveSessionCreateFunc(golibdave.NewSession),
	)
}

// DaveAvailable reports whether this build can wire real DAVE encryption. It is
// true in the DAVE build (this file) and false in the default stub build.
func DaveAvailable() bool { return true }
