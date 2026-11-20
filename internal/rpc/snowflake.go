package rpc

import "regexp"

// snowflakePattern is the shape of a Discord snowflake id: an unsigned 64-bit
// decimal, so 1–20 digits and nothing else.
var snowflakePattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// isSnowflake is THE Discord-id shape check every RPC uses — a Player's
// Discord User ID, a guild id, a voice channel id — so an accidental non-id
// (a name, a pasted URL, a signed or over-long number) fails fast here
// instead of reaching storage or a Discord call.
func isSnowflake(s string) bool {
	return snowflakePattern.MatchString(s)
}
