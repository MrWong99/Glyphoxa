import { useEffect, useRef, useState, type ReactNode } from "react";
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

// ADD_BOT_HINT points a not-a-member precondition failure back at the Add-Glyphoxa
// action, which renders at the FOOT of this card (below the Save button) — so the
// direction word must match its placement. It is appended ONLY to the not-a-member
// message; the no-token precondition ("save the token first") is already complete
// guidance and adding "add the Bot" there would be wrong (the token, not
// membership, is what's missing).
const ADD_BOT_HINT =
  "Use the Add Glyphoxa to your server button at the foot of this card, then paste the invite again.";

// NOT_A_MEMBER_MARK picks the not-a-member precondition out of the two messages
// that share the FailedPrecondition code (ADR-0047): only that one earns the
// add-bot hint. Matched as a substring so minor server rewording still routes it.
const NOT_A_MEMBER_MARK = /not a member/i;

// RESOLVE_DEBOUNCE_MS delays the authed invite resolve so editing/typing a
// discord.gg URL does not fire a bot-token GET /invites per keystroke (mostly
// 404s that burn Discord's rate budget, #245). A single paste settles after one
// tick; the channel-link fill stays instant (it needs no round-trip).
const RESOLVE_DEBOUNCE_MS = 400;

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

  // currentCode is the invite code the input parses to RIGHT NOW — null when it
  // is empty, a channel link, or unparseable. Mutation callbacks land in
  // completion order, so a slow resolve for a superseded paste can arrive after a
  // newer one; the callbacks bail unless their request code still matches this
  // ref (latest-wins, #245). Without it a stale success overwrites a newer picker
  // (picking then fills the WRONG guild) or wipes a current parse error, and a
  // stale error paints over a valid picker.
  const currentCode = useRef<string | null>(null);
  // debounceTimer holds the pending resolve; every change clears it first, so a
  // burst of keystrokes collapses to one resolve of the final value (#245).
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(debounceTimer.current), []);

  const resolve = useMutation(ProviderService.method.resolveGuildInvite, {
    onSuccess: (res, variables) => {
      if (variables.inviteCode !== currentCode.current) return; // superseded
      setPicker(res);
      setLinkError(null);
    },
    onError: (err, variables) => {
      if (variables.inviteCode !== currentCode.current) return; // superseded
      setLinkError(inviteErrorMessage(err));
    },
  });

  const onPaste = (value: string) => {
    setLink(value);
    // Every change cancels a still-pending resolve so typing does not fire one
    // GET /invites per keystroke.
    clearTimeout(debounceTimer.current);
    if (!value.trim()) {
      currentCode.current = null;
      setLinkError(null);
      return;
    }
    const parsed = parseDiscordLink(value);
    if (!parsed) {
      currentCode.current = null;
      setLinkError("Couldn't read that link — paste a Discord channel or invite link.");
      return;
    }
    if (parsed.kind === "channel") {
      // Self-describing: both ids are in the URL, fill locally with no round-trip.
      currentCode.current = null;
      setLinkError(null);
      onFill(parsed.guildId, parsed.channelId);
      return;
    }
    // Invite: only the code is known; the guild + channels resolve server-side.
    // currentCode updates NOW (before the debounce) so a still-in-flight earlier
    // resolve is recognised as superseded when it lands.
    const code = parsed.code;
    currentCode.current = code;
    setLinkError(null);
    debounceTimer.current = setTimeout(
      () => resolve.mutate({ inviteCode: code }),
      RESOLVE_DEBOUNCE_MS,
    );
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
// message, ADR-0047). The add-bot hint is appended ONLY to the not-a-member
// message — the no-token message stands alone as its own complete guidance.
function inviteErrorMessage(err: unknown): ReactNode {
  const code = err instanceof ConnectError ? err.code : undefined;
  const raw =
    err instanceof ConnectError ? err.rawMessage : err instanceof Error ? err.message : String(err);
  if (code === Code.NotFound) {
    return "That invite looks invalid or expired.";
  }
  if (code === Code.FailedPrecondition) {
    if (NOT_A_MEMBER_MARK.test(raw)) {
      return (
        <>
          <span>{raw}</span> <span>{ADD_BOT_HINT}</span>
        </>
      );
    }
    return <span>{raw}</span>;
  }
  return "Couldn't resolve that invite. Please try again.";
}
