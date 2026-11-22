import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createConnectQueryKey, useQuery, useMutation, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Lock, Plus, Search, TriangleAlert, Users, Volume2 } from "lucide-react";

import { CampaignService, VoiceService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, GetCampaignRosterResponse } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Input";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Skeleton } from "@/components/ui/Skeleton";
import { Tooltip } from "@/components/ui/Tooltip";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { AgentEditor } from "./AgentEditor";
import { rosterRetry, useCampaignPanelState, useCampaignScreen } from "./campaignContext";
import { useCastReadiness } from "./castReadiness";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { NewNpcDialog } from "./NewNpcDialog";
import { RosterPrep } from "./RosterPrep";
import type { RosterEntry } from "./rosterPrep";
import { voiceName } from "./VoicePicker";

import "./cast.css";

// The Cast view of the Campaign screen (#71): the design's Butler/NPC editor on
// the live CampaignService roster + CRUD RPCs (ADR-0039). The Butler is
// required, role-locked and undeletable (ADR-0009); its Address-Only switch is
// forced on and disabled (ADR-0024). NPCs are added, edited and deleted; every
// edit round-trips to the DB and the roster re-reads it back after each
// mutation invalidates the query.
//
// The cast studio shaped it into four files: this panel (the roster list, the
// mode switch and the wiring), AgentEditor (one member's form), NewNpcDialog
// (how an NPC starts) and RosterPrep (the Readiness view). What the panel
// reaches through the host instead of props: cross-view jumps
// (useCampaignScreen) and the ?agent= deep link (useScreenNav).

// The roster asks for a filter once it no longer fits a glance.
const FILTER_ABOVE = 8;

// speakerVar maps a server-assigned palette slot onto the 6-colour speaker
// palette (tokens.css --speaker-1..6). The slot is 0-based; the palette is 1-based.
function speakerVar(slot: number): string {
  return `var(--speaker-${(slot % 6) + 1})`;
}

function isButler(a: Agent): boolean {
  return a.role === "butler";
}

type CastMode = "edit" | "prep";

export function CastPanel() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const nav = useScreenNav();
  const { openNode, openView } = useCampaignScreen();

  // The same read the host resolves the campaign from (one cache entry, one
  // fetch); the panel observes it itself so it owns its own loading and error
  // states and mounts on nothing but its RPCs.
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const roster = useMemo(() => rosterQuery.data?.roster ?? [], [rosterQuery.data]);
  // The EXACT key that read writes (transport + empty input + finite), so a
  // created NPC can be put on the cached roster before the re-read lands.
  const transport = useTransport();
  const rosterKey = useMemo(
    () =>
      createConnectQueryKey({
        schema: CampaignService.method.getCampaignRoster,
        transport,
        input: {},
        cardinality: "finite",
      }),
    [transport],
  );
  const campaignLanguage = rosterQuery.data?.campaign?.language;

  // Live ElevenLabs voice catalog (#70). The query is non-blocking: a missing
  // key / failed catalog leaves voices empty and the editor still renders the
  // agent's persisted voice id, so the screen never breaks on a degraded TTS.
  const voicesQuery = useQuery(VoiceService.method.listVoices, {});
  const voiceNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const v of voicesQuery.data?.voices ?? []) m.set(v.voiceId, voiceName(v));
    return m;
  }, [voicesQuery.data]);

  // Who is session-ready, graded once for the markers on the roster, the count
  // on the Readiness segment and the editor's callout.
  const prep = useCastReadiness(roster);

  // Selection: the chosen agent, defaulting to the first roster member (the
  // Butler) until the operator picks another. Falls back to the Butler when the
  // selected NPC is deleted. Held in the host's panel state so it is still
  // there when the GM comes back from another view (the panel itself unmounts
  // on a sidebar switch) — and gone after an Active Campaign switch.
  const [selectedId, setSelectedId] = useCampaignPanelState<string | null>("cast.selectedId", null);
  const selected = roster.find((a) => a.id === selectedId) ?? roster[0];

  // The editor (default), or the Readiness view (#544). The editor stays the
  // default because adding and shaping NPCs is the common act; readiness is
  // what you check before a session.
  const [castMode, setCastMode] = useCampaignPanelState<CastMode>("cast.mode", "edit");

  const [attentionOnly, setAttentionOnly] = useState(false);
  const [filter, setFilter] = useState("");
  const [newOpen, setNewOpen] = useState(false);
  // A concept typed into the New NPC dialog, waiting for that NPC's editor to
  // mount and draft a personality from it.
  const [concept, setConcept] = useState<{ agentId: string; prompt: string } | null>(null);
  // Bumped to remount the editor on the stored agent: the GM agreed to discard
  // its edits, and the form must really let go of them.
  const [editorEpoch, setEditorEpoch] = useState(0);

  // Creating a Character NPC auto-creates its wiki entry, and deleting one
  // unlinks that entry (ADR-0008 second amendment) — both are Knowledge Graph
  // writes, so a roster mutation must drop the KG reads too. Without this the
  // Health panel pairs a fresh roster with a stale graph and invents findings.
  const invalidateRoster = () => {
    void invalidateMethodQueries(queryClient, CampaignService.method.getCampaignRoster);
    invalidateKnowledgeReads(queryClient);
  };

  // A rejected delete otherwise leaves the roster silently unchanged
  // (ADR-0017: a failed mutation is a toast).
  const deleteAgent = useMutation(CampaignService.method.deleteAgent, {
    onSuccess: () => {
      setSelectedId(null); // fall back to the Butler
      void invalidateRoster();
    },
    onError: (err) => toast.error(t("campaign.couldntDeleteNpc", { message: errorMessage(err) })),
  });

  // Unsaved editor text is guarded on EVERY path that unmounts or re-seeds the
  // agent editor (W10): picking another roster member (it remounts per
  // selection, key={id}), switching to the Readiness view, and adding an NPC
  // (whose success selects the new one). The editor reports its dirtiness; a
  // dirty move asks first. Leaving via the sidebar is held by the route layer
  // off the same flag (useDirtyGuard → lib/navigationGuard).
  const edits = useDirtyGuard({
    title: t("campaign.discardEditsTitle"),
    description: t("campaign.discardEditsDesc", { name: selected?.name ?? "" }),
    confirmLabel: t("campaign.discardEditsConfirm"),
  });
  const { guard, markDirty } = edits;
  const editorDirty = useRef(false);
  const onEditorDirty = useCallback(
    (dirty: boolean) => {
      editorDirty.current = dirty;
      markDirty(dirty);
    },
    [markDirty],
  );

  const selectAgent = (id: string) => {
    if (id === selected?.id) return;
    guard(() => setSelectedId(id));
  };

  // "Add NPC" opens the dialog; nothing is created until it is submitted. The
  // guard still asks first: creating selects the new NPC, which replaces the
  // open editor. A confirmed discard remounts the editor on the stored agent
  // right away — otherwise a cancelled dialog would leave the discarded text on
  // screen with nothing guarding it any more.
  const openNewNpc = () => {
    const wasDirty = editorDirty.current;
    guard(() => {
      if (wasDirty) setEditorEpoch((n) => n + 1);
      setNewOpen(true);
    });
  };

  // ?agent=<id> deep link: open the editor on that Agent, once. The route
  // guarantees the param only ever arrives on this view (subviewRoute's
  // beforeLoad), so this is a pure consume-then-strip: take the selection, then
  // strip the param (replace, no history entry) so the URL does not pin it and
  // a later jump to the same Agent fires again. The selection itself goes
  // through the edits guard — a jump that lands while the open editor is dirty
  // asks before replacing it.
  const agentParam = nav.search.agent;
  const { setSearch } = nav;
  useEffect(() => {
    if (!agentParam) return;
    // Already open on that Agent's editor: nothing would be replaced, so there
    // is nothing to ask about.
    const alreadyOpen = castMode === "edit" && agentParam === selected?.id;
    if (!alreadyOpen) {
      guard(() => {
        setSelectedId(agentParam);
        setCastMode("edit");
      });
    }
    setSearch({ agent: undefined }, { replace: true });
    // The param is the trigger; guard and the setters are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentParam]);

  // On a phone the editor sits BELOW the roster: picking a row there changes
  // something the GM cannot see. Bring the editor into view when the selection
  // changes while the two are stacked (campaign.css stacks them at 720px).
  const editorPane = useRef<HTMLDivElement>(null);
  const lastSelected = useRef<string | null>(null);
  useEffect(() => {
    const id = selected?.id ?? null;
    const previous = lastSelected.current;
    lastSelected.current = id;
    if (previous === null || previous === id || castMode !== "edit") return;
    if (typeof window.matchMedia !== "function" || !window.matchMedia("(max-width: 720px)").matches) return;
    editorPane.current?.scrollIntoView?.({ block: "start", behavior: "smooth" });
  }, [selected?.id, castMode]);

  const npcCount = roster.filter((a) => !isButler(a)).length;
  const attentionCount = prep.summary.attention.length;
  // The toggle cannot stay on once nobody needs attention: it would hide the
  // whole roster behind a filter whose button has just disappeared.
  const attentionFilter = attentionOnly && attentionCount > 0;
  const filterOffered = roster.length > FILTER_ABOVE;
  const needle = filterOffered ? filter.trim().toLocaleLowerCase() : "";
  const visible = roster.filter((a) => {
    if (attentionFilter && (prep.byAgent.get(a.id)?.ready ?? true)) return false;
    if (needle === "") return true;
    return [a.name, a.title, ...a.aliases].join(" ").toLocaleLowerCase().includes(needle);
  });

  return (
    <QueryBoundary
      query={rosterQuery}
      errorTitle={t("cast.loadErrorTitle")}
      skeleton={
        // One status region shaped like the view: the roster's rows beside the
        // editor's fields.
        <Skeleton>
          <div className="gx-roster-layout">
            <Skeleton.Row count={4} />
            <Skeleton lines={6} />
          </div>
        </Skeleton>
      }
    >
      <div className="gx-cast">
        <SegmentedControl
          label={t("campaign.castViewGroup")}
          value={castMode}
          onChange={(next) => {
            // Roster → Readiness unmounts the editor; the other way has
            // nothing to lose.
            if (next === "prep") guard(() => setCastMode("prep"));
            else setCastMode("edit");
          }}
          options={[
            { value: "edit", label: t("campaign.castRoster") },
            {
              value: "prep",
              label: t("campaign.castPrep"),
              // The number of NPCs that are not session-ready; no number at
              // all when everyone is.
              count: attentionCount > 0 ? attentionCount : undefined,
            },
          ]}
        />
        {castMode === "prep" ? (
          <RosterPrep
            roster={roster}
            onSelectAgent={(id) => {
              setSelectedId(id);
              setCastMode("edit");
            }}
            onOpenNode={openNode}
            onOpenKnowledge={() => openView("knowledge")}
            onAddNpc={() => {
              setCastMode("edit");
              setNewOpen(true);
            }}
          />
        ) : (
          <div className="gx-roster-layout">
            <div className="gx-roster gx-cast__roster">
              {npcCount > 0 && (
                <>
                  <Button variant="secondary" block iconStart={<Plus size={15} />} onClick={openNewNpc}>
                    {t("campaign.addNpc")}
                  </Button>
                  <p className="gx-cast__summary">
                    <span>
                      {npcCount === 1 ? t("cast.summaryNpcsOne") : t("cast.summaryNpcs", { n: npcCount })}
                    </span>
                    {attentionCount > 0 ? (
                      <>
                        <span aria-hidden="true">·</span>
                        <Tooltip content={t("cast.summaryAttentionHint")}>
                          <button
                            type="button"
                            className="gx-cast-chip gx-cast-chip--warning"
                            aria-pressed={attentionFilter}
                            onClick={() => setAttentionOnly(!attentionFilter)}
                          >
                            <TriangleAlert size={12} aria-hidden="true" />
                            {attentionCount === 1
                              ? t("cast.summaryAttentionOne")
                              : t("cast.summaryAttention", { n: attentionCount })}
                          </button>
                        </Tooltip>
                      </>
                    ) : (
                      // "All ready" is only claimed once the wiki-side checks
                      // have actually been read, for every NPC on the list.
                      prep.complete && (
                        <>
                          <span aria-hidden="true">·</span>
                          <span>{t("cast.summaryAllReady")}</span>
                        </>
                      )
                    )}
                  </p>
                </>
              )}

              {filterOffered && (
                <Input
                  type="search"
                  aria-label={t("cast.filterLabel")}
                  placeholder={t("cast.filterPlaceholder")}
                  icon={<Search />}
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  autoComplete="off"
                />
              )}

              {visible.length > 0 && (
                <ul className="gx-cast__list" role="list" aria-label={t("cast.rosterAria")}>
                  {visible.map((a) => (
                    <li key={a.id}>
                      <RosterRow
                        agent={a}
                        active={selected?.id === a.id}
                        entry={prep.byAgent.get(a.id)}
                        voice={voiceNames.get(a.voice)}
                        onSelect={() => selectAgent(a.id)}
                      />
                    </li>
                  ))}
                </ul>
              )}

              {visible.length === 0 && needle !== "" && (
                <EmptyState
                  size="inline"
                  title={t("cast.filterNoMatch", { query: filter.trim() })}
                  action={
                    <Button variant="secondary" size="sm" onClick={() => setFilter("")}>
                      {t("cast.filterClear")}
                    </Button>
                  }
                />
              )}

              {npcCount === 0 && (
                <EmptyState
                  icon={<Users size={22} />}
                  title={t("cast.emptyTitle")}
                  description={t("cast.emptyDesc")}
                  action={
                    <Button iconStart={<Plus size={15} />} onClick={openNewNpc}>
                      {t("campaign.addNpc")}
                    </Button>
                  }
                />
              )}
            </div>

            {/* Editor pane — keyed by the selected agent so its local form resets
                when the selection changes. */}
            <div className="gx-cast__editor" ref={editorPane}>
              {selected && (
                <AgentEditor
                  key={`${selected.id}:${editorEpoch}`}
                  agent={selected}
                  voicesQuery={voicesQuery}
                  campaignLanguage={campaignLanguage}
                  prep={prep.byAgent.get(selected.id)}
                  onSaved={() => void invalidateRoster()}
                  onDelete={isButler(selected) ? undefined : () => deleteAgent.mutate({ id: selected.id })}
                  deleting={deleteAgent.isPending}
                  onDirtyChange={onEditorDirty}
                  autoDraft={concept?.agentId === selected.id ? concept.prompt : undefined}
                  onAutoDraftStarted={() => setConcept(null)}
                  onOpenNode={openNode}
                  onOpenWiki={() => openView("knowledge")}
                />
              )}
            </div>
          </div>
        )}
        <NewNpcDialog
          open={newOpen}
          onOpenChange={setNewOpen}
          voicesQuery={voicesQuery}
          campaignLanguage={campaignLanguage}
          onCreated={(agent, prompt) => {
            setNewOpen(false);
            setAttentionOnly(false);
            setFilter("");
            setConcept(prompt ? { agentId: agent.id, prompt } : null);
            // Put her on the roster at once (the re-read below reconciles), so
            // the selection lands on HER editor instead of falling back to the
            // Butler's for the length of a round trip.
            queryClient.setQueryData<GetCampaignRosterResponse>(rosterKey, (prev) =>
              prev && !prev.roster.some((a) => a.id === agent.id)
                ? { ...prev, roster: [...prev.roster, agent] }
                : prev,
            );
            setSelectedId(agent.id);
            void invalidateRoster();
          }}
        />
        {edits.dialog}
      </div>
    </QueryBoundary>
  );
}

// RosterRow is one cast member in the list: who they are, the voice they speak
// with, and — for an NPC that is not session-ready — a warning marker. The
// marker's reasons are in a tooltip (hover or keyboard focus on the row) and,
// for a screen reader, in the row's own text; the full list with its fixes is
// the callout at the top of the editor the row opens.
function RosterRow({
  agent,
  active,
  entry,
  voice,
  onSelect,
}: {
  agent: Agent;
  active: boolean;
  entry: RosterEntry | undefined;
  /** The voice's name from the catalog; undefined when unset or unknown. */
  voice: string | undefined;
  onSelect: () => void;
}) {
  const { t } = useI18n();
  const butler = isButler(agent);
  const missing = entry && !entry.exempt ? entry.checks.filter((c) => !c.ok).map((c) => t(c.missingKey)) : [];

  return (
    // The tooltip is held shut, not removed, for a ready member: the row must
    // stay the same DOM node when its last warning clears (a save can do that
    // while focus is on it).
    <Tooltip
      side="top"
      align="end"
      disabled={missing.length === 0}
      content={
        <span className="gx-cast__row-tip">
          <strong>{t("cast.rowNeedsTitle")}</strong>
          {missing.map((label) => (
            <span key={label}>{label}</span>
          ))}
        </span>
      }
    >
      <button
        type="button"
        className="gx-roster__item"
        data-active={active ? "true" : undefined}
        data-role={agent.role}
        aria-current={active ? "true" : undefined}
        onClick={onSelect}
      >
        {butler ? (
          <Avatar name={agent.name} size="sm" />
        ) : (
          <span className="gx-roster__dot" style={{ background: speakerVar(agent.speakerColor) }} aria-hidden />
        )}
        <span className="gx-roster__meta">
          <span className="gx-roster__name">{agent.name}</span>
          {(agent.title || voice) && (
            <span className="gx-cast__row-sub">
              {agent.title && <span className="gx-roster__title">{agent.title}</span>}
              {voice && (
                <span className="gx-cast__row-voice">
                  <Volume2 size={11} aria-hidden="true" />
                  <span aria-hidden="true">{voice}</span>
                  <span className="gx-visually-hidden">{t("cast.rowVoice", { name: voice })}</span>
                </span>
              )}
            </span>
          )}
        </span>
        {butler ? (
          <Badge variant="gold" size="sm" dot>
            <Lock size={11} /> {t("campaign.badgeButler")}
          </Badge>
        ) : (
          agent.addressOnly && (
            <Badge variant="neutral" size="sm" title={t("campaign.badgeAddressOnlyTitle")}>
              {t("campaign.badgeAddressOnly")}
            </Badge>
          )
        )}
        {missing.length > 0 && (
          <span className="gx-cast__row-warn">
            <TriangleAlert size={15} aria-hidden="true" />
            <span className="gx-visually-hidden">{t("cast.rowNeeds", { checks: missing.join(", ") })}</span>
          </span>
        )}
      </button>
    </Tooltip>
  );
}
