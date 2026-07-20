package discordguild

// CheckAdminAt exposes the base-URL seam so tests can point the checker at a
// fake Discord REST server instead of the live API.
var CheckAdminAt = checkAdmin
