package main

import (
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec"
)

// version is the release this binary was built from. Release builds stamp it
// with `-ldflags "-X main.version=v0.16.0"` (the Dockerfile's VERSION build-arg,
// fed by release-image.yml; `make build`/`make build-live` stamp `git describe`).
// An unstamped build reports "dev", refined by the VCS revision Go embeds when
// it builds inside a git checkout.
var version = "dev"

// buildVersion returns the stamped version, or for an unstamped build "dev"
// plus the short VCS revision when the toolchain recorded one.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return "dev-" + s.Value[:12]
			}
		}
	}
	return version
}

// runVersion implements `glyphoxa version`: the version plus whether the voice
// build tags were linked, so an operator can tell a stub build from a live one
// without starting a session.
func runVersion(w io.Writer) {
	fmt.Fprintf(w, "glyphoxa %s (opus codec: %t, dave: %t)\n", buildVersion(), codec.Available, gxvoice.DaveAvailable())
}

// stubBuildWarnings names the voice capabilities a build lacks. A binary built
// without `-tags "opus dave nolibopusfile"` (plain `go build`, `make build`)
// links the stub codec and/or the stub DAVE wiring: it boots, but every voice
// session fails (no Opus transcoding; Discord rejects unencrypted voice with
// close code 4017). Empty for a live build.
func stubBuildWarnings(codecAvailable, daveAvailable bool) []string {
	var w []string
	if !codecAvailable {
		w = append(w, "built without -tags opus: the stub Opus codec is linked and voice sessions cannot decode or play audio")
	}
	if !daveAvailable {
		w = append(w, "built without -tags dave: DAVE end-to-end encryption is not linked and Discord will reject the voice connection")
	}
	return w
}

// logBuildInfo logs the version at boot and, for a Mode that hosts the voice
// loop (voice/all), one WARN per missing voice capability — the from-source
// `make build` binary is otherwise indistinguishable from a live one until the
// first session fails. Use `make build-live` (or the container image).
func logBuildInfo(log *slog.Logger, mode string) {
	log.Info("glyphoxa starting", "version", buildVersion(), "mode", mode)
	if mode != "voice" && mode != "all" {
		return
	}
	for _, msg := range stubBuildWarnings(codec.Available, gxvoice.DaveAvailable()) {
		log.Warn("stub voice build: "+msg, "fix", "build with `make build-live` or use the container image")
	}
}
