package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// userUsage documents the `glyphoxa user` subcommand (ADR-0055): the operator
// surface for open-Admission-Mode revocation. Suspension is the open-mode
// counterpart of removing a snowflake from the allowlist — enforced on the
// very next request by the per-request session re-check, without deleting
// sessions, and fully reversible.
const userUsage = `usage: glyphoxa user <suspend|unsuspend> <discord-user-id>

  suspend    lock the user out of the web tier on their next request (open
             Admission Mode revocation, ADR-0055); their sessions survive,
             dormant. Their tenant-operator GM identity (transcript labels,
             GM slash commands, Butler voice addressing) drops out within the
             ~1-minute GM snapshot refresh — but a snowflake ALSO on
             GLYPHOXA_OPERATOR_IDS keeps GM through the allowlist half of the
             union; remove it from the env list too for a full GM revocation.
  unsuspend  restore access; surviving sessions work again immediately

Connection string is read from $GLYPHOXA_DATABASE_URL (or $DATABASE_URL).`

// RunUser is the entry point for the `user` subcommand. args are the arguments
// after "user".
func RunUser(ctx context.Context, args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, userUsage)
		return fmt.Errorf("user: missing subcommand")
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(userUsage)
		return nil
	}

	suspend := false
	switch args[0] {
	case "suspend":
		suspend = true
	case "unsuspend":
	default:
		fmt.Fprintln(os.Stderr, userUsage)
		return fmt.Errorf("user: unknown subcommand %q", args[0])
	}
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, userUsage)
		return fmt.Errorf("user %s: exactly one Discord user id is required", args[0])
	}
	discordID := args[1]

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("user: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("user: open db: %w", err)
	}
	defer pool.Close()
	st := storage.New(pool)

	if err := st.SetUserSuspended(ctx, discordID, suspend); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("user %s: no user with Discord id %q", args[0], discordID)
		}
		return fmt.Errorf("user %s: %w", args[0], err)
	}
	if suspend {
		fmt.Printf("suspended %s — web access ends on their next request; tenant-operator GM identity drops within ~1 min (env-allowlist GM survives); sessions kept, reversible with `glyphoxa user unsuspend`\n", discordID)
	} else {
		fmt.Printf("unsuspended %s — surviving sessions work again immediately\n", discordID)
	}
	return nil
}
