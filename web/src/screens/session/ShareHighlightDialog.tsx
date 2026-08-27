import { useEffect, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { Share2, Send, Radio, Download } from "lucide-react";
import { toast } from "sonner";

import { timestampMs } from "@bufbuild/protobuf/wkt";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Highlight } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";

// clipFileSlug names a downloaded clip by its moment ("2026-08-23-22-56-36"),
// falling back to the highlight id when the timestamp is unset — filesystem-safe
// (no colons) and sortable.
function clipFileSlug(h: Highlight): string {
  const ms = h.startsAt ? Number(timestampMs(h.startsAt)) : null;
  return ms != null ? new Date(ms).toISOString().slice(0, 19).replace(/[T:]/g, "-") : h.id;
}

// ShareHighlightDialog is the GM's Discord-delivery surface for ONE promoted
// Session Highlight (#310, Epic 8, ADR-0051 GM-only sharing). It offers the three
// decided modes and nothing auto-posts — every share is an explicit GM click:
//   - Post to channel: pick a guild text channel (pre-selected to the campaign's
//     last choice) and upload the clip as a file (public in that channel).
//   - Replay in voice: play the clip into the LIVE voice channel — disabled with a
//     hint when no session is live, since there is nothing to play into.
//   - Download: the existing cookie-authed clip blob path, no RPC involved.
//
// Only promoted Highlights are shareable, so the Session screen renders this only
// for promoted rows (the renderActions slot). The channel list + last-choice come
// from ListShareChannels; the delivery goes through ShareHighlight.
export function ShareHighlightDialog({
  highlight,
  sessionLive,
}: {
  highlight: Highlight;
  sessionLive: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [channelId, setChannelId] = useState("");

  // The guild's channels + the campaign's last-shared channel, loaded only once the
  // GM opens the panel (a share is a rare, deliberate action).
  const channels = useQuery(SessionService.method.listShareChannels, {}, { enabled: open });

  // Pre-select the campaign's last choice once the list lands; fall back to the
  // first channel so the Post action is never stuck with an empty selection. Only
  // seeds while the GM has not yet picked (channelId still "").
  useEffect(() => {
    if (!channels.data || channelId !== "") return;
    const last = channels.data.lastShareChannelId;
    if (last && channels.data.channels.some((c) => c.id === last)) {
      setChannelId(last);
    } else if (channels.data.channels[0]) {
      setChannelId(channels.data.channels[0].id);
    }
  }, [channels.data, channelId]);

  const share = useMutation(SessionService.method.shareHighlight, {
    onSuccess: () => setOpen(false),
    onError: (err: Error) => toast.error(t("session.couldntShare", { message: errorMessage(err) })),
  });

  const postToChannel = () => {
    share.mutate(
      { id: highlight.id, mode: { case: "textChannelId", value: channelId } },
      { onSuccess: () => toast.success(t("session.postedToDiscord")) },
    );
  };

  const replayInVoice = () => {
    share.mutate(
      { id: highlight.id, mode: { case: "voiceReplay", value: true } },
      { onSuccess: () => toast.success(t("session.replayingInVoice")) },
    );
  };

  if (!open) {
    return (
      <Button
        variant="secondary"
        size="sm"
        iconStart={<Share2 size={14} />}
        onClick={() => setOpen(true)}
      >
        {t("session.share")}
      </Button>
    );
  }

  return (
    <div className="gx-highlight-share" role="group" aria-label={t("session.shareGroupLabel")}>
      {channels.isError ? (
        <p className="gx-highlight-share__error" role="alert">
          {errorMessage(channels.error)}
        </p>
      ) : (
        <div className="gx-highlight-share__channel">
          <Select
            aria-label={t("session.discordChannel")}
            placeholder={channels.isPending ? t("session.loadingChannels") : t("session.pickChannel")}
            options={(channels.data?.channels ?? []).map((c) => ({ value: c.id, label: `#${c.name}` }))}
            value={channelId || undefined}
            onValueChange={setChannelId}
            disabled={channels.isPending}
          />
          <Button
            variant="primary"
            size="sm"
            iconStart={<Send size={14} />}
            onClick={postToChannel}
            disabled={channelId === "" || share.isPending}
          >
            {t("session.postToChannel")}
          </Button>
        </div>
      )}

      <div className="gx-highlight-share__replay">
        <Button
          variant="secondary"
          size="sm"
          iconStart={<Radio size={14} />}
          onClick={replayInVoice}
          disabled={!sessionLive || share.isPending}
        >
          {t("session.replayInVoice")}
        </Button>
        {!sessionLive && (
          <span className="gx-highlight-share__hint">{t("session.replayHint")}</span>
        )}
      </div>

      <a
        className="gx-highlight-share__download"
        href={`/api/v1/highlights/${highlight.id}/clip`}
        // Named by its moment — a downloads folder of identical
        // "highlight.wav"s silently overwrites or numbers itself.
        download={`highlight-${clipFileSlug(highlight)}.wav`}
      >
        <Download size={14} aria-hidden="true" /> {t("session.download")}
      </a>

      <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
        {t("common.close")}
      </Button>
    </div>
  );
}
