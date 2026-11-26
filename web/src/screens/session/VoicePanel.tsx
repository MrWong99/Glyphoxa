import { useId, useMemo, useState, type CSSProperties } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { ChevronDown, ChevronUp, Volume2, VolumeX } from "lucide-react";

import { SessionService, CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, AgentReadiness } from "@gen/glyphoxa/management/v1/management_pb";
import { Avatar } from "@/components/ui/Avatar";
import { errorMessage } from "@/lib/connectError";
import { useI18n, type TFunc } from "@/i18n";
import { useMuteCache } from "./muteCache";
import { agentIndex, speakerVar } from "./speakers";

// The Voice control panel (#211): the Session screen's rail listing every
// Agent of the Active Campaign (Butler first, then Character NPCs, from the
// getCampaignRoster read — NOT the voiced wirenpc Roster). Each Character NPC row
// toggles that Agent's mute in the live Voice Session; the Address-Only Butler is
// never voiced (ADR-0009), so its row carries no mute toggle and neither the count
// nor the mute-all button reckons with it. The mute-all button mutes every
// voiceable NPC while any is voicing (unmuted), else unmutes them all. With no live
// Voice Session the toggles are disabled — mute is only actionable while a session
// is live, mirroring the /glyphoxa mute commands.
//
// Three things make it usable DURING play:
//
//   - it stays on screen: a sticky rail beside the transcript on a wide screen,
//     and at 900px and below a strip directly under the control bar that the GM
//     can fold open (Mute all stays on the strip itself);
//   - the Agent that is speaking right now is marked, in its own colour, so
//     the mute target is the row that is lit — not a name to find in a list;
//   - idle, each NPC says whether it is actually ready (GetRosterReadiness, the
//     same figures the voice loop injects) instead of a uniform "Ready" that
//     was true of nobody in particular.

function isButler(a: Agent): boolean {
  return a.role === "butler";
}

type Idle = { text: string; warn: boolean };

/** What an NPC's row says while no session is live. */
function idleState(
  agent: Agent,
  readiness: AgentReadiness | undefined,
  readinessState: "pending" | "error" | "ready",
  t: TFunc,
): Idle {
  if (agent.voice === "") return { text: t("session.stateNoVoice"), warn: true };
  if (!readiness) {
    // Still loading, or the read failed: say only what is known.
    return {
      text: readinessState === "pending" ? t("session.stateChecking") : t("session.stateVoiceSet"),
      warn: false,
    };
  }
  if (!readiness.linked) return { text: t("session.stateNoEntry"), warn: true };
  if (readiness.factCount === 0) return { text: t("session.stateNoFacts"), warn: true };
  return {
    text:
      readiness.factCount === 1
        ? t("session.stateReadyFactsOne")
        : t("session.stateReadyFacts", { n: readiness.factCount }),
    warn: false,
  };
}

export function VoicePanel({
  active,
  mutedIds,
  speakingName = "",
}: {
  active: boolean;
  mutedIds: string[];
  /** The name the relay says is speaking right now ("" when nobody is). */
  speakingName?: string;
}) {
  const { t } = useI18n();
  const { replace } = useMuteCache();
  const bodyId = useId();
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {});
  const roster = useMemo(() => rosterQuery.data?.roster ?? [], [rosterQuery.data]);
  const muted = useMemo(() => new Set(mutedIds), [mutedIds]);

  // Readiness is a pre-session question; while live the rows show mute state.
  // A soft read: retry:false, and a failure only costs the detail.
  const readinessQuery = useQuery(
    CampaignService.method.getRosterReadiness,
    {},
    { enabled: !active, retry: false },
  );
  const readinessByAgent = useMemo(() => {
    const m = new Map<string, AgentReadiness>();
    for (const r of readinessQuery.data?.agents ?? []) m.set(r.agentId, r);
    return m;
  }, [readinessQuery.data]);
  const readinessState = readinessQuery.isPending ? "pending" : readinessQuery.isError ? "error" : "ready";

  // The strip is folded by default where it is a strip (900px and below); on a
  // wide screen the rail is always open and this state is not consulted.
  const [collapsed, setCollapsed] = useState(true);

  // Both mutations patch the SHARED getSession cache from their authoritative
  // response, so the panel (and any other reader) reflects the new set instantly.
  // A failed flip surfaces (ADR-0017: sonner) — a GM muting an NPC mid-scene
  // must know the NPC is still speaking, not read silence as success.
  const setAgentMute = useMutation(SessionService.method.setAgentMute, {
    onSuccess: (res) => replace(res.mutedAgentIds),
    onError: (err) => toast.error(t("session.couldntSetMute", { message: errorMessage(err) })),
  });
  const setAllMute = useMutation(SessionService.method.setAllMute, {
    onSuccess: (res) => replace(res.mutedAgentIds),
    onError: (err) => toast.error(t("session.couldntSetMute", { message: errorMessage(err) })),
  });

  // The Address-Only Butler is never voiced and never a mute target (ADR-0009); the
  // server's mute surface (voicedAgents) excludes it, so it never appears in
  // mutedIds. The panel mirrors that: it counts and flips over the VOICEABLE
  // Character NPCs only — otherwise the always-unmuted Butler would keep anyVoicing
  // pinned true and the mute-all button could never flip to "Unmute all".
  const voiceable = useMemo(() => roster.filter((a) => !isButler(a)), [roster]);
  const total = voiceable.length;
  // "voicing" is the panel's label for unmuted; the count reflects live voicing,
  // so it is 0 while idle (no session is producing audio).
  const voicing = active ? voiceable.filter((a) => !muted.has(a.id)).length : 0;
  const anyVoicing = voicing > 0;
  const pending = setAgentMute.isPending || setAllMute.isPending;

  // The relay names the speaker by its label, so it is matched the way a
  // transcript line is: by name or alias.
  const speaking = useMemo(
    () => (active && speakingName ? agentIndex(roster).get(speakingName.trim().toLocaleLowerCase()) : undefined),
    [active, speakingName, roster],
  );

  const muteToggle = (a: Agent, isMuted: boolean) => (
    <button
      type="button"
      className="gx-voice-row__toggle"
      data-muted={isMuted || undefined}
      disabled={!active || pending}
      aria-label={isMuted ? t("session.unmuteAgent", { name: a.name }) : t("session.muteAgent", { name: a.name })}
      onClick={() => setAgentMute.mutate({ agentId: a.id, muted: !isMuted })}
    >
      {isMuted ? <Volume2 size={15} /> : <VolumeX size={15} />}
    </button>
  );

  return (
    <aside
      className="gx-voice-panel"
      aria-label={t("session.voiceControl")}
      data-collapsed={collapsed || undefined}
    >
      <div className="gx-voice-panel__head">
        <div className="gx-voice-panel__titles">
          <span className="gx-overline">{t("session.voiceControl")}</span>
          <h2 className="gx-voice-panel__title">{t("session.npcVoices")}</h2>
          <p className="gx-voice-panel__count" data-testid="voicing-count">
            {voicing === 1
              ? t("session.voicingCountOne", { voicing, total })
              : t("session.voicingCount", { voicing, total })}
          </p>
        </div>

        <button
          type="button"
          className="gx-voice-panel__all"
          disabled={!active || pending}
          onClick={() => setAllMute.mutate({ muted: anyVoicing })}
        >
          {anyVoicing ? <VolumeX size={15} /> : <Volume2 size={15} />}
          {anyVoicing ? t("session.muteAll") : t("session.unmuteAll")}
        </button>

        {/* Shown only where the panel is a strip (CSS). */}
        <button
          type="button"
          className="gx-voice-panel__fold"
          aria-expanded={!collapsed}
          aria-controls={bodyId}
          onClick={() => setCollapsed((c) => !c)}
        >
          {collapsed ? <ChevronDown size={16} aria-hidden="true" /> : <ChevronUp size={16} aria-hidden="true" />}
          <span className="gx-visually-hidden">
            {collapsed ? t("session.voicesShow") : t("session.voicesHide")}
          </span>
        </button>
      </div>

      {/* Who is speaking, for a screen reader. A live region announces a change
          to content it already holds, so this one is always mounted while the
          session runs and only its text follows the speaker. */}
      {active && (
        <div className="gx-visually-hidden" role="status" aria-live="polite" data-testid="voice-announcer">
          {speaking && !isButler(speaking) ? t("session.typingSpeaking", { name: speaking.name }) : ""}
        </div>
      )}

      {/* The folded strip still names — and can mute — whoever is speaking: on
          a phone that is the one control the GM reaches for mid-scene. Hidden
          wherever the rows themselves are on screen (CSS). */}
      {speaking && !isButler(speaking) && (
        <div
          className="gx-voice-panel__now"
          style={{ "--gx-speaker": speakerVar(speaking.speakerColor) } as CSSProperties}
          data-testid="voice-now"
        >
          <span className="gx-voice-row__dot" data-speaking aria-hidden />
          <span className="gx-voice-panel__now-name">{t("session.typingSpeaking", { name: speaking.name })}</span>
          {muteToggle(speaking, muted.has(speaking.id))}
        </div>
      )}

      <div className="gx-voice-panel__body" id={bodyId}>
        {rosterQuery.isError && (
          <div className="gx-voice-panel__error" role="alert">
            <p>{t("session.rosterLoadFailed", { message: errorMessage(rosterQuery.error) })}</p>
            <button type="button" className="gx-voice-panel__retry" onClick={() => void rosterQuery.refetch()}>
              {t("session.retry")}
            </button>
          </div>
        )}

        <ul className="gx-voice-panel__rows">
          {roster.map((a) => {
            const butler = isButler(a);
            const isMuted = active && !butler && muted.has(a.id);
            const isSpeaking = speaking?.id === a.id;
            // The Butler is Address-Only: it never voices, so it carries a neutral
            // state and no mute toggle (muting it would hit ErrAgentNotInCampaign →
            // a silently swallowed CodeNotFound), while Character NPCs toggle mute.
            // "address-only" is internal vocabulary — users see plain language.
            // An unmuted NPC reads "Voice on", never "Speaking", unless the relay
            // says it is producing audio right now: unmuted only means it MAY speak.
            const idle = !active && !butler ? idleState(a, readinessByAgent.get(a.id), readinessState, t) : null;
            const state = isSpeaking
              ? t("session.stateSpeaking")
              : butler
                ? t("session.butlerState")
                : idle
                  ? idle.text
                  : isMuted
                    ? t("session.stateMuted")
                    : t("session.stateVoiceOn");
            return (
              <li
                key={a.id}
                className="gx-voice-row"
                data-muted={isMuted || undefined}
                data-speaking={isSpeaking || undefined}
                data-testid="voice-row"
                style={butler ? undefined : ({ "--gx-speaker": speakerVar(a.speakerColor) } as CSSProperties)}
              >
                {butler ? (
                  <Avatar name={a.name} size="sm" />
                ) : (
                  <span className="gx-voice-row__dot" data-speaking={isSpeaking || undefined} aria-hidden />
                )}
                <span className="gx-voice-row__meta">
                  <span className="gx-voice-row__name">{a.name}</span>
                  <span
                    className="gx-voice-row__state"
                    data-warn={idle?.warn || undefined}
                    // The speaking marker changes under the GM's eyes; the
                    // panel's own status region above says it aloud.
                  >
                    {state}
                  </span>
                </span>
                {!butler && muteToggle(a, isMuted)}
              </li>
            );
          })}
        </ul>

        <p className="gx-voice-panel__hint">{t("session.mutedHint")}</p>
      </div>
    </aside>
  );
}
