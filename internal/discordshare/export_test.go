package discordshare

// Base-URL seams so tests point the calls at a fake Discord REST server instead of
// the live API.
var (
	ListTextChannelsAt = listTextChannels
	PostFileAt         = postFile
)
