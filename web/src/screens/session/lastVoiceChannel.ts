// The voice channel a session was last successfully started in, remembered per
// Discord server in this browser. It is the middle rung of the Session screen's
// channel precedence — picked → last used → the Tenant's Default Voice Channel →
// none — so a GM who always plays in "The Tavern" does not re-pick it on every
// page load, without anyone having to promote it to the Tenant-wide default.
//
// Client-side on purpose (for now): it is a per-GM convenience, not shared
// configuration. Storage can be unavailable (privacy mode, blocked site data),
// so every access is guarded and the feature simply degrades to "no last used".

const key = (guildId: string) => `gx-voice-channel:${guildId}`;

/** The channel id last started in for this guild, or null when none is known. */
export function readLastVoiceChannel(guildId: string): string | null {
  if (!guildId) return null;
  try {
    return localStorage.getItem(key(guildId)) || null;
  } catch {
    return null;
  }
}

/** Records the channel a session just started in. A no-op without a guild or channel. */
export function rememberVoiceChannel(guildId: string, channelId: string): void {
  if (!guildId || !channelId) return;
  try {
    localStorage.setItem(key(guildId), channelId);
  } catch {
    // storage unavailable — the pick still applied to this start
  }
}
