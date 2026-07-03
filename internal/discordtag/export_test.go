package discordtag

// ResolveAt exposes the base-URL seam so tests can point the resolver at a
// fake Discord REST server instead of the live API.
var ResolveAt = resolve
