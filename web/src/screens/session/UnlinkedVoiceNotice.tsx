import { useMemo, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { UserPlus } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { useI18n } from "@/i18n";
import type { BindPrefill } from "./SessionBindAffordance";

// "N people in voice are not linked": the people sitting in the voice channel
// who have no Character in this campaign. Until one is linked, what they say is
// transcribed under the anonymous "Player / GM" label and the NPCs cannot call
// them by name — and nothing on the screen said so until the transcript was
// already full of it.
//
// The list is read ONCE, when the session connects (staleTime Infinity, no
// refetch on focus): ListDiscordVoiceMembers fans out to Discord per member, so
// it is a check at the start of the evening, not a live roster. Linking someone
// refreshes the Character list, which takes them off the notice.
//
// The server lists the channel the running session joined (the stored Default
// Voice Channel only while idle), so these are the people at tonight's table.
//
// One honest limit, from the wire as it is today: the GM is in the channel too
// and is nobody's Character. The web console is not told which Discord User
// the signed-in GM is, so it cannot leave them out by itself: "Not a player"
// does, and is remembered for the campaign in this browser.

const ignoredKey = (campaignId: string) => `gx-voice-not-players:${campaignId}`;
const dismissedKey = (sessionId: string) => `gx-unlinked-dismissed:${sessionId}`;

function readIgnored(campaignId: string): string[] {
  try {
    const raw = localStorage.getItem(ignoredKey(campaignId));
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : [];
  } catch {
    return [];
  }
}

function writeIgnored(campaignId: string, ids: string[]): void {
  try {
    localStorage.setItem(ignoredKey(campaignId), JSON.stringify(ids));
  } catch {
    // storage unavailable — the choice still holds for this visit
  }
}

function readDismissed(sessionId: string): boolean {
  try {
    return sessionStorage.getItem(dismissedKey(sessionId)) === "1";
  } catch {
    return false;
  }
}

export function UnlinkedVoiceNotice({
  sessionId,
  campaignId,
  connected,
  onLink,
}: {
  sessionId: string;
  campaignId: string;
  /** The Bot is in the channel: only then is "who is here" a real answer. */
  connected: boolean;
  /** Opens the bind form seeded with this person. */
  onLink: (person: BindPrefill) => void;
}) {
  const { t } = useI18n();

  const once = { enabled: connected, retry: false, staleTime: Infinity, refetchOnWindowFocus: false } as const;
  const membersQuery = useQuery(CampaignService.method.listDiscordVoiceMembers, {}, once);
  // Not "once": a link made here invalidates this read, which is what removes
  // the linked person from the notice.
  const charactersQuery = useQuery(
    CampaignService.method.listCharacters,
    {},
    { enabled: connected, retry: false },
  );

  const [ignored, setIgnored] = useState<string[]>(() => readIgnored(campaignId));
  const [dismissed, setDismissed] = useState(() => readDismissed(sessionId));

  const unlinked = useMemo(() => {
    // Both reads must have landed: with no Character list everyone would look
    // unlinked.
    if (!membersQuery.data || !charactersQuery.data) return [];
    const bound = new Set(charactersQuery.data.characters.map((c) => c.discordUserId));
    const skip = new Set(ignored);
    return membersQuery.data.members.filter((m) => !bound.has(m.discordUserId) && !skip.has(m.discordUserId));
  }, [membersQuery.data, charactersQuery.data, ignored]);

  if (!connected || dismissed || unlinked.length === 0) return null;

  const ignore = (id: string) => {
    const next = [...new Set([...ignored, id])];
    setIgnored(next);
    writeIgnored(campaignId, next);
  };

  const dismiss = () => {
    setDismissed(true);
    try {
      sessionStorage.setItem(dismissedKey(sessionId), "1");
    } catch {
      // storage unavailable — dismissed for this mount
    }
  };

  return (
    <Alert
      tone="info"
      className="gx-session__unlinked"
      title={
        unlinked.length === 1 ? t("session.unlinkedTitleOne") : t("session.unlinkedTitle", { n: unlinked.length })
      }
      onDismiss={dismiss}
    >
      <p className="gx-session__unlinked-desc">{t("session.unlinkedDesc")}</p>
      <ul className="gx-session__unlinked-list" data-testid="unlinked-voice">
        {unlinked.map((m) => {
          const name = m.displayName || m.discordUserId;
          return (
            <li key={m.discordUserId}>
              <span className="gx-session__unlinked-name">{name}</span>
              <Button
                variant="secondary"
                size="sm"
                iconStart={<UserPlus size={14} />}
                aria-label={t("session.unlinkedLinkAria", { name })}
                onClick={() => onLink({ discordUserId: m.discordUserId, name })}
              >
                {t("session.unlinkedLink")}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                aria-label={t("session.unlinkedNotPlayerAria", { name })}
                onClick={() => ignore(m.discordUserId)}
              >
                {t("session.unlinkedNotPlayer")}
              </Button>
            </li>
          );
        })}
      </ul>
    </Alert>
  );
}
