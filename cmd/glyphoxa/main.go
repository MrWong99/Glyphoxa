package main

import (
	"context"
	"fmt"
	"os"
)

// main is a thin dispatcher. The full Mode dispatcher (all/web/voice) and root
// command surface belong to the control-plane task (#6); this branch wires only
// the `migrate` subcommand (ADR-0031) so the persistence layer is usable
// standalone. When #6 lands its root command, `migrate` routing folds into it.
func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "migrate" {
		if err := RunMigrate(context.Background(), args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	// Non-migrate invocations are handled by the Mode dispatcher (task #6).
}
