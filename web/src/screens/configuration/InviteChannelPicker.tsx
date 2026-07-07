import { Hash } from "lucide-react";

import type { VoiceChannel } from "@gen/glyphoxa/management/v1/management_pb";

// InviteChannelPicker — the list of voice channels ResolveGuildInvite returned
// for a pasted invite (#105, ADR-0047). It shows the resolved guild's name as a
// header and one clickable row per voice channel (its name + snowflake); picking
// a row fills the Guild ID + Voice channel ID fields via onPick. An empty list
// (a guild with no voice channels) renders an explanatory line instead of rows.

export function InviteChannelPicker({
  guildName,
  channels,
  onPick,
}: {
  guildName: string;
  channels: VoiceChannel[];
  onPick: (channelId: string) => void;
}) {
  return (
    <div className="gx-invite-picker">
      <div className="gx-invite-picker__header">
        <span className="gx-overline">Voice channels in</span>
        <span className="gx-invite-picker__guild">{guildName}</span>
      </div>
      {channels.length === 0 ? (
        <p className="gx-invite-picker__empty">No voice channels in {guildName}.</p>
      ) : (
        <ul className="gx-invite-picker__list">
          {channels.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                className="gx-invite-picker__row"
                onClick={() => onPick(c.id)}
              >
                <span className="gx-invite-picker__icon">
                  <Hash size={15} />
                </span>
                <span className="gx-invite-picker__name">{c.name}</span>
                <span className="gx-invite-picker__id">{c.id}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
