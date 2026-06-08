//go:build !dave

package voice

import "github.com/disgoorg/disgo/bot"

// DaveOption is the default (stub) build of the DAVE wiring: without the `dave`
// build tag libdave is not linked, so it returns a no-op [bot.ConfigOpt] and the
// client connects without end-to-end encryption. Discord rejects unencrypted
// voice with close code 4017 (ADR-0006), so this build is for tests and tooling
// only — never a production Voice Instance, which must build with `-tags dave`.
// Callers that must guarantee DAVE should check [DaveAvailable] and refuse to
// start when it is false.
func DaveOption() bot.ConfigOpt {
	// A zero-arg voice-manager opt is a valid no-op: the client builds without a
	// DAVE session-create func, so connections run unencrypted.
	return bot.WithVoiceManagerConfigOpts()
}

// DaveAvailable reports whether this build can wire real DAVE encryption. It is
// false in the default stub build (this file) and true in the `-tags dave` build.
func DaveAvailable() bool { return false }
