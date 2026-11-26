package discordguild

import (
	"context"
	"log/slog"
)

// CheckAdminAt exposes the base-URL seam so tests can point the checker at a
// fake Discord REST server instead of the live API.
func CheckAdminAt(ctx context.Context, token, guildID, userID, baseURL string, log *slog.Logger) error {
	_, err := checkAdmin(ctx, token, guildID, userID, baseURL, log)
	return err
}

// CheckAdminGuildAt is CheckAdminAt returning the guild name as well.
var CheckAdminGuildAt = checkAdmin
