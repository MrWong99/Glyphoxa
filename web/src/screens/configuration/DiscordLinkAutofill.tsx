import { useState, type ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { Link as LinkIcon } from "lucide-react";

import {
  ProviderService,
  type ResolveGuildInviteResponse,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { parseDiscordLink } from "@/lib/discordLink";
import { InviteChannelPicker } from "./InviteChannelPicker";

// DiscordLinkAutofill — the Configuration Discord card's "Paste a Discord link"
// field (#101/#105, ADR-0047). It parses the paste client-side (no network) and
// takes one of two paths:
//   - a channel deep-link carries BOTH snowflakes, so it fills the two ID fields
//     directly via onFill;
//   - an invite link carries only a code, which it resolves server-side
//     (ResolveGuildInvite, with the decrypted Bot token) to the guild's voice
//     channels, then renders a picker; picking a channel fills the IDs via onFill.
// onFill MUST be the Configuration screen's dirty-tracking edit path — a raw
// setState would let a config refetch clobber the fill. A failed resolve leaves
// the fields and any previously-resolved picker untouched.

// ADD_BOT_HINT points a not-a-member / no-token precondition failure back at the
// Add-Glyphoxa action; the invite can only resolve once the Bot is in the guild.
const ADD_BOT_HINT = "Use the Add Glyphoxa to your server button above, then paste the invite again.";

export function DiscordLinkAutofill({
  onFill,
}: {
  onFill: (guildId: string, channelId: string) => void;
}) {
  const [link, setLink] = useState("");
  const [linkError, setLinkError] = useState<ReactNode>(null);
  // The picker is held in local state, not read off the mutation's `data`: a
  // later failed resolve must leave the previous picker standing (ADR-0047), and
  // react-query clears `data` on the next mutate. onSuccess is the only writer.
  const [picker, setPicker] = useState<ResolveGuildInviteResponse | null>(null);

  const resolve = useMutation(ProviderService.method.resolveGuildInvite, {
    onSuccess: (res) => {
      setPicker(res);
      setLinkError(null);
    },
    onError: (err) => setLinkError(inviteErrorMessage(err)),
  });

  const onPaste = (value: string) => {
    setLink(value);
    if (!value.trim()) {
      setLinkError(null);
      return;
    }
    const parsed = parseDiscordLink(value);
    if (!parsed) {
      setLinkError("Couldn't read that link — paste a Discord channel or invite link.");
      return;
    }
    if (parsed.kind === "channel") {
      // Self-describing: both ids are in the URL, fill locally with no round-trip.
      setLinkError(null);
      onFill(parsed.guildId, parsed.channelId);
      return;
    }
    // Invite: only the code is known; the guild + channels resolve server-side.
    setLinkError(null);
    resolve.mutate({ inviteCode: parsed.code });
  };

  return (
    <div className="gx-discord__link">
      <Input
        label="Paste a Discord link"
        placeholder="https://discord.com/channels/… or discord.gg/…"
        icon={<LinkIcon size={15} />}
        hint="Paste a channel link to fill both IDs, or an invite link to pick a voice channel."
        error={linkError}
        value={link}
        onChange={(e) => onPaste(e.target.value)}
      />
      {resolve.isPending && <p className="gx-discord__resolving">Resolving invite…</p>}
      {picker && (
        <InviteChannelPicker
          guildName={picker.guildName}
          channels={picker.voiceChannels}
          onPick={(channelId) => onFill(picker.guildId, channelId)}
        />
      )}
    </div>
  );
}

// inviteErrorMessage maps a ResolveGuildInvite failure onto the inline message.
// NotFound is a plain invalid/expired line; FailedPrecondition renders the SERVER
// message verbatim (no-token vs not-a-member share the code and differ only by
// message, ADR-0047) plus the add-bot hint.
function inviteErrorMessage(err: unknown): ReactNode {
  const code = err instanceof ConnectError ? err.code : undefined;
  const raw =
    err instanceof ConnectError ? err.rawMessage : err instanceof Error ? err.message : String(err);
  if (code === Code.NotFound) {
    return "That invite looks invalid or expired.";
  }
  if (code === Code.FailedPrecondition) {
    return (
      <>
        <span>{raw}</span> <span>{ADD_BOT_HINT}</span>
      </>
    );
  }
  return "Couldn't resolve that invite. Please try again.";
}
