import { useMemo } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { Volume2, VolumeX } from "lucide-react";

import { SessionService, CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Avatar } from "@/components/ui/Avatar";
import { useMuteCache } from "./muteCache";

// The Voice control panel (#211): the Session screen's right rail listing every
// Agent of the Active Campaign (Butler first, then Character NPCs, from the
// getCampaignRoster read — NOT the voiced wirenpc Roster). Each row toggles that
// Agent's mute in the live Voice Session; the mute-all button mutes everyone while
// any Agent is voicing (unmuted), else unmutes everyone. With no live Voice
// Session every row shows unmuted and the toggles are disabled — mute is only
// actionable while a session is live, mirroring the /glyphoxa mute commands.

// speakerVar maps a server-assigned palette slot onto the 6-colour speaker palette
// (tokens.css --speaker-1..6). The slot is 0-based; the palette is 1-based.
function speakerVar(slot: number): string {
  return `var(--speaker-${(slot % 6) + 1})`;
}

function isButler(a: Agent): boolean {
  return a.role === "butler";
}

export function VoicePanel({ active, mutedIds }: { active: boolean; mutedIds: string[] }) {
  const { replace } = useMuteCache();
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {});
  const roster = useMemo(() => rosterQuery.data?.roster ?? [], [rosterQuery.data]);
  const muted = useMemo(() => new Set(mutedIds), [mutedIds]);

  // Both mutations patch the SHARED getSession cache from their authoritative
  // response, so the panel (and any other reader) reflects the new set instantly.
  const setAgentMute = useMutation(SessionService.method.setAgentMute, {
    onSuccess: (res) => replace(res.mutedAgentIds),
  });
  const setAllMute = useMutation(SessionService.method.setAllMute, {
    onSuccess: (res) => replace(res.mutedAgentIds),
  });

  const total = roster.length;
  // "voicing" is the panel's label for unmuted; the count reflects live voicing,
  // so it is 0 while idle (no session is producing audio).
  const voicing = active ? roster.filter((a) => !muted.has(a.id)).length : 0;
  const anyVoicing = voicing > 0;
  const pending = setAgentMute.isPending || setAllMute.isPending;

  return (
    <aside className="gx-voice-panel" aria-label="Voice control">
      <div className="gx-voice-panel__head">
        <span className="gx-overline">Voice control</span>
        <h2 className="gx-voice-panel__title">NPC voices</h2>
        <p className="gx-voice-panel__count" data-testid="voicing-count">
          {voicing} of {total} voicing
        </p>
      </div>

      <button
        type="button"
        className="gx-voice-panel__all"
        disabled={!active || pending}
        onClick={() => setAllMute.mutate({ muted: anyVoicing })}
      >
        {anyVoicing ? <VolumeX size={15} /> : <Volume2 size={15} />}
        {anyVoicing ? "Mute all" : "Unmute all"}
      </button>

      <ul className="gx-voice-panel__rows">
        {roster.map((a) => {
          const isMuted = active && muted.has(a.id);
          const state = isMuted ? "Muted" : isButler(a) ? "Butler · voicing" : "Voicing";
          return (
            <li key={a.id} className="gx-voice-row" data-muted={isMuted || undefined} data-testid="voice-row">
              {isButler(a) ? (
                <Avatar name={a.name} size="sm" />
              ) : (
                <span className="gx-voice-row__dot" style={{ background: speakerVar(a.speakerColor) }} aria-hidden />
              )}
              <span className="gx-voice-row__meta">
                <span className="gx-voice-row__name">{a.name}</span>
                <span className="gx-voice-row__state">{state}</span>
              </span>
              <button
                type="button"
                className="gx-voice-row__toggle"
                data-muted={isMuted || undefined}
                disabled={!active || pending}
                aria-label={isMuted ? `Unmute ${a.name}` : `Mute ${a.name}`}
                onClick={() => setAgentMute.mutate({ agentId: a.id, muted: !isMuted })}
              >
                {isMuted ? <Volume2 size={15} /> : <VolumeX size={15} />}
              </button>
            </li>
          );
        })}
      </ul>

      <p className="gx-voice-panel__hint">
        Muted NPCs stay in the scene but won&apos;t speak aloud. Unmute any voice mid-session.
      </p>
    </aside>
  );
}
