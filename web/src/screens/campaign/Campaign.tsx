import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Lock, Plus, Sparkles, Trash2, Volume2 } from "lucide-react";

import { CampaignService, VoiceService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, Voice, ToolGrant } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Avatar } from "@/components/ui/Avatar";
import { Combobox } from "@/components/ui/Combobox";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { useI18n } from "@/i18n";
import { playAudioBlob } from "@/lib/audio";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { KnowledgePanel } from "./KnowledgePanel";
import { MapsPanel } from "./MapsPanel";
import { PlayersPanel } from "./PlayersPanel";
import { RosterPrep } from "./RosterPrep";
import { ProposalsPanel } from "./ProposalsPanel";
import { PlanningPanel } from "./PlanningPanel";

import "./campaign.css";

// The Campaign screen (#71) backs the design's Butler/NPC editor on the live
// CampaignService roster + CRUD RPCs (ADR-0039). The Butler is required, role-
// locked and undeletable (ADR-0009); its Address-Only switch is forced on and
// disabled (ADR-0024). NPCs are added, edited and deleted; every edit round-trips
// to the DB and the roster re-reads it back after each mutation invalidates the
// query.

// The voice dropdown is LIVE ElevenLabs ListVoices data (#70, VoiceService);
// each option's value is the vendor voice id persisted on the agent and its
// label is "ElevenLabs · Name". Preview voice synthesizes a short sample.

// speakerVar maps a server-assigned palette slot onto the 6-colour speaker
// palette (tokens.css --speaker-1..6). The slot is 0-based; the palette is 1-based.
function speakerVar(slot: number): string {
  return `var(--speaker-${(slot % 6) + 1})`;
}

function isButler(a: Agent): boolean {
  return a.role === "butler";
}

// CampaignDeepLink is the palette's arrival vocabulary (#591): view selects a
// sub-view, node opens the Knowledge panel focused on that entry. Both optional;
// the route strips the params after onDeepLinkHandled so nothing is sticky.
export type CampaignDeepLink = { view?: string; node?: string };

export function Campaign({
  deepLink,
  onDeepLinkHandled,
}: {
  deepLink?: CampaignDeepLink;
  onDeepLinkHandled?: () => void;
} = {}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data, status, error } = useQuery(CampaignService.method.getCampaignRoster, {});
  const roster = useMemo(() => data?.roster ?? [], [data]);

  // Live ElevenLabs voice catalog (#70). The query is non-blocking: a missing
  // key / failed catalog leaves voices empty and the editor still renders the
  // agent's persisted voice id, so the screen never breaks on a degraded TTS.
  const voicesQuery = useQuery(VoiceService.method.listVoices, {});
  const voices = useMemo(() => voicesQuery.data?.voices ?? [], [voicesQuery.data]);

  // Selection: the chosen agent, defaulting to the first roster member (the
  // Butler) until the operator picks another. Falls back to the Butler when the
  // selected NPC is deleted.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = roster.find((a) => a.id === selectedId) ?? roster[0];

  // Cast (roster editor), Knowledge (KG entries), or Players (Character ↔ Discord
  // User bindings, #279) — the design's seg-control beside the title. Cast is the
  // default so the roster is what loads first (#71).
  const [view, setView] = useState<
    "cast" | "knowledge" | "maps" | "players" | "proposals" | "planning"
  >("cast");
  // Within Cast: the editor (default), or the prep readiness view (#544). The
  // editor stays the default because adding and shaping NPCs is the common act;
  // readiness is what you check before a session.
  const [castMode, setCastMode] = useState<"edit" | "prep">("edit");
  // The entry a readiness mark asked to open, handed to the Knowledge panel so
  // "Entry has content" lands ON that entry instead of dumping the GM on the list.
  const [focusNodeID, setFocusNodeID] = useState<string | null>(null);

  // Palette deep link (#591): ?node=… opens the Knowledge panel focused on that
  // entry (the RosterPrep onOpenNode path — KnowledgePanel resolves the id once
  // its list loads); a bare ?view=… just selects the sub-view. Consumed once,
  // then onDeepLinkHandled strips the params so the URL doesn't pin the view.
  const dlView = deepLink?.view;
  const dlNode = deepLink?.node;
  useEffect(() => {
    if (!dlView && !dlNode) return;
    if (dlNode) {
      setFocusNodeID(dlNode);
      setView("knowledge");
    } else if (
      dlView === "cast" ||
      dlView === "knowledge" ||
      dlView === "maps" ||
      dlView === "players" ||
      dlView === "proposals" ||
      dlView === "planning"
    ) {
      setView(dlView);
    }
    onDeepLinkHandled?.();
    // onDeepLinkHandled is a stable route-level callback; the params are the triggers.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dlView, dlNode]);

  // Creating a Character NPC auto-creates its wiki entry, and deleting one
  // unlinks that entry (ADR-0008 second amendment) — both are Knowledge Graph
  // writes, so a roster mutation must drop the KG reads too. Without this the
  // Health panel pairs a fresh roster with a stale graph and invents findings.
  const invalidateRoster = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.getCampaignRoster,
        cardinality: "finite",
      }),
    });
    invalidateKnowledgeReads(queryClient);
  };

  const createAgent = useMutation(CampaignService.method.createAgent, {
    onSuccess: (res) => {
      void invalidateRoster();
      if (res.agent) setSelectedId(res.agent.id);
    },
  });
  const deleteAgent = useMutation(CampaignService.method.deleteAgent, {
    onSuccess: () => {
      setSelectedId(null); // fall back to the Butler
      void invalidateRoster();
    },
  });

  const campaign = data?.campaign;
  const npcs = roster.filter((a) => !isButler(a));

  return (
    <div className="gx-campaign-screen">
      <header className="gx-campaign-screen__header">
        <div className="gx-campaign-screen__title-row">
          <h1>{campaign?.name ?? t("campaign.fallbackTitle")}</h1>
          <div className="gx-seg" role="tablist" aria-label={t("campaign.viewTablist")}>
            <button
              type="button"
              role="tab"
              aria-selected={view === "cast"}
              data-active={view === "cast" ? "true" : undefined}
              onClick={() => setView("cast")}
            >
              {t("campaign.tabCast")}
            </button>
            {/* The internal "Knowledge Graph" never faces the GM — the tab reads
                "World wiki" (the copy-simplification glossary). */}
            <button
              type="button"
              role="tab"
              aria-selected={view === "knowledge"}
              data-active={view === "knowledge" ? "true" : undefined}
              onClick={() => setView("knowledge")}
            >
              {t("campaign.tabWiki")}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "maps"}
              data-active={view === "maps" ? "true" : undefined}
              onClick={() => setView("maps")}
            >
              {t("campaign.tabMaps")}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "players"}
              data-active={view === "players" ? "true" : undefined}
              onClick={() => setView("players")}
            >
              {t("campaign.tabPlayers")}
            </button>
            {/* "Knowledge Proposals" simplifies to "Suggestions" for the same
                reason the graph became a wiki: GMs, not developers. */}
            <button
              type="button"
              role="tab"
              aria-selected={view === "proposals"}
              data-active={view === "proposals" ? "true" : undefined}
              onClick={() => setView("proposals")}
            >
              {t("campaign.tabSuggestions")}
            </button>
            {/* Butler planning chat (#592, ADR-0062). */}
            <button
              type="button"
              role="tab"
              aria-selected={view === "planning"}
              data-active={view === "planning" ? "true" : undefined}
              onClick={() => setView("planning")}
            >
              {t("chat.tabPlanning")}
            </button>
          </div>
        </div>
        <div className="gx-campaign-screen__sub">
          {campaign?.system && <span className="gx-campaign-screen__system">{campaign.system}</span>}
          <span className="gx-campaign-screen__lede">
            {view === "knowledge"
              ? t("campaign.ledeWiki")
              : view === "players"
                ? t("campaign.ledePlayers")
                : view === "proposals"
                  ? t("campaign.ledeSuggestions")
                  : view === "planning"
                    ? t("chat.ledePlanning")
                    : t("campaign.ledeCast")}
          </span>
        </div>
      </header>

      {view === "knowledge" ? (
        <KnowledgePanel
          focusNodeID={focusNodeID}
          onFocusHandled={() => setFocusNodeID(null)}
          onOpenCast={(agentID) => {
            setSelectedId(agentID);
            setCastMode("edit");
            setView("cast");
          }}
        />
      ) : view === "maps" ? (
        <MapsPanel
          onOpenNode={(id) => {
            setFocusNodeID(id);
            setView("knowledge");
          }}
        />
      ) : view === "players" ? (
        <PlayersPanel />
      ) : view === "proposals" ? (
        <ProposalsPanel />
      ) : view === "planning" ? (
        // The ChatService RPCs are campaign-scoped (ADR-0062), so the panel
        // waits for the roster read to hand it the campaign id.
        campaign ? (
          <PlanningPanel campaignId={campaign.id} />
        ) : status === "error" ? (
          <p className="gx-campaign__error" role="alert">
            {t("campaign.loadError", { message: error.message })}
          </p>
        ) : (
          <div className="gx-skeleton" data-testid="planning-loading" />
        )
      ) : status === "pending" ? (
        <div className="gx-skeleton" data-testid="roster-loading" />
      ) : status === "error" ? (
        <p className="gx-campaign__error" role="alert">
          {t("campaign.loadError", { message: error.message })}
        </p>
      ) : (
        <>
        <div className="gx-kg-modes" role="group" aria-label={t("campaign.castViewGroup")}>
          <button
            type="button"
            className="gx-kg-chip"
            aria-pressed={castMode === "edit"}
            onClick={() => setCastMode("edit")}
          >
            {t("campaign.castRoster")}
          </button>
          <button
            type="button"
            className="gx-kg-chip"
            aria-pressed={castMode === "prep"}
            onClick={() => setCastMode("prep")}
          >
            {t("campaign.castPrep")}
          </button>
        </div>
        {castMode === "prep" ? (
          <RosterPrep
            roster={roster}
            onSelectAgent={(id) => {
              setSelectedId(id);
              setCastMode("edit");
            }}
            onOpenNode={(id) => {
              setFocusNodeID(id);
              setView("knowledge");
            }}
            onOpenKnowledge={() => setView("knowledge")}
          />
        ) : (
        <div className="gx-roster-layout">
          {/* Roster list */}
          <div className="gx-roster">
          {roster.map((a) => (
            <button
              key={a.id}
              type="button"
              className="gx-roster__item"
              data-active={selected?.id === a.id ? "true" : undefined}
              data-role={a.role}
              onClick={() => setSelectedId(a.id)}
            >
              {isButler(a) ? (
                <Avatar name={a.name} size="sm" />
              ) : (
                <span
                  className="gx-roster__dot"
                  style={{ background: speakerVar(a.speakerColor) }}
                  aria-hidden
                />
              )}
              <span className="gx-roster__meta">
                <span className="gx-roster__name">{a.name}</span>
                {a.title && <span className="gx-roster__title">{a.title}</span>}
              </span>
              {isButler(a) ? (
                <Badge variant="gold" size="sm" dot>
                  <Lock size={11} /> {t("campaign.badgeButler")}
                </Badge>
              ) : (
                a.addressOnly && (
                  <Badge variant="neutral" size="sm">
                    {t("campaign.badgeAddressOnly")}
                  </Badge>
                )
              )}
            </button>
          ))}

          <button
            type="button"
            className="gx-roster__add"
            disabled={createAgent.isPending}
            onClick={() =>
              // The placeholder NAME is persisted, so it is created in the GM's
              // display language — the roster shows what they will rename anyway.
              createAgent.mutate({ name: t("campaign.newNpcName"), title: "", persona: "", voice: "", addressOnly: false })
            }
          >
            <Plus size={15} /> {t("campaign.addNpc")}
          </button>

          {npcs.length === 0 && (
            <p className="gx-roster__empty">{t("campaign.rosterEmpty")}</p>
          )}
        </div>

        {/* Editor pane — keyed by the selected agent so its local form resets when
            the selection changes. */}
        {selected && (
          <AgentEditor
            key={selected.id}
            agent={selected}
            voices={voices}
            onSaved={() => void invalidateRoster()}
            onDelete={
              isButler(selected) ? undefined : () => deleteAgent.mutate({ id: selected.id })
            }
            deleting={deleteAgent.isPending}
          />
        )}
        </div>
        )}
        </>
      )}
    </div>
  );
}

// AgentEditor edits one roster member. It holds the editable fields in local
// state (seeded from the agent) and saves them via UpdateAgent. For the Butler
// the role is locked, it cannot be deleted, and Address-Only is forced on with a
// disabled switch (ADR-0009 / ADR-0024).
function AgentEditor({
  agent,
  voices,
  onSaved,
  onDelete,
  deleting,
}: {
  agent: Agent;
  voices: Voice[];
  onSaved: () => void;
  onDelete?: () => void;
  deleting: boolean;
}) {
  const { t } = useI18n();
  const butler = isButler(agent);
  const [name, setName] = useState(agent.name);
  const [title, setTitle] = useState(agent.title);
  const [persona, setPersona] = useState(agent.persona);
  const [voice, setVoice] = useState(agent.voice);
  const [addressOnly, setAddressOnly] = useState(agent.addressOnly);

  const update = useMutation(CampaignService.method.updateAgent, {
    onSuccess: () => {
      onSaved();
      toast.success(t("campaign.savedAgent", { name: name || agent.name }));
    },
    onError: (err) => toast.error(t("common.couldntSave", { message: err.message })),
  });
  const preview = useMutation(VoiceService.method.previewVoice);

  // On-demand persona drafting (#479): runs ONLY when the GM presses Generate.
  // The draft lands in the local persona field for review — nothing is saved
  // until the GM presses "Save changes". Failures render an inline cue like the
  // voice-preview one.
  const [draftPrompt, setDraftPrompt] = useState("");
  const [draftError, setDraftError] = useState<string | null>(null);
  const generate = useMutation(CampaignService.method.generatePersona);
  const draftPersona = async () => {
    setDraftError(null);
    try {
      // The live form name/title ride along so the draft addresses what the GM
      // has typed, not the stored cast entry — which may still be the "New NPC"
      // placeholder when the GM hasn't saved yet (#480).
      const res = await generate.mutateAsync({ agentId: agent.id, prompt: draftPrompt, name, title });
      setPersona(res.persona);
    } catch (err) {
      setDraftError(err instanceof Error ? err.message : String(err));
    }
  };

  // Options come from the live catalog: value = vendor voice id, label =
  // "ElevenLabs · Name". The agent's persisted voice id is kept as a bare option
  // even when the catalog is empty/stale, so the current selection always shows.
  const voiceOpts = useMemo(() => {
    const opts = voices.map((v) => ({ value: v.voiceId, label: v.label || v.name || v.voiceId }));
    if (voice && !opts.some((o) => o.value === voice)) opts.unshift({ value: voice, label: voice });
    return opts;
  }, [voices, voice]);

  // Preview failures — a degraded-TTS RPC rejection or a blocked/failed
  // play() — render an inline cue instead of vanishing (#154, mirrors the
  // save status treatment from #94).
  const [previewError, setPreviewError] = useState<string | null>(null);
  const playPreview = async () => {
    if (!voice) return;
    setPreviewError(null);
    try {
      const res = await preview.mutateAsync({ voiceId: voice, text: "" });
      await playAudioBlob(res.audio, res.mimeType);
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err));
    }
  };

  const save = () =>
    update.mutate({
      id: agent.id,
      name,
      title,
      persona,
      voice,
      // The Butler is always Address-Only; the server enforces this regardless.
      addressOnly: butler ? true : addressOnly,
      aliases: agent.aliases,
    });

  return (
    <Card accent className="gx-editor">
      <div className="gx-editor__head">
        {butler ? <Avatar name={agent.name} size="lg" /> : <Avatar name={name || agent.name} size="lg" />}
        <div className="gx-editor__head-meta">
          {butler ? (
            <Badge variant="gold" size="sm" dot>
              <Sparkles size={11} /> {t("campaign.badgeRequired")}
            </Badge>
          ) : (
            <Badge variant="neutral" size="sm">
              {t("campaign.badgeNpc")}
            </Badge>
          )}
          <span className="gx-editor__role">{butler ? t("campaign.roleButler") : t("campaign.roleNpc")}</span>
        </div>
      </div>

      <div className="gx-editor__grid">
        <Input label={t("campaign.nameLabel")} value={name} onChange={(e) => setName(e.target.value)} />
        <Input label={t("campaign.titleLabel")} value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t("campaign.titlePlaceholder")} />
      </div>

      <div className="gx-field">
        {/* The wire field is still `persona`; the GM sees plain language. */}
        <label className="gx-field__label" htmlFor="gx-persona">
          {t("campaign.personaLabel")}
        </label>
        <textarea
          id="gx-persona"
          className="gx-input gx-textarea"
          rows={4}
          value={persona}
          onChange={(e) => setPersona(e.target.value)}
        />
        <span className="gx-field__hint">{t("campaign.personaHint")}</span>
        {!butler && (
          <div className="gx-editor__draft">
            <Input
              label={t("campaign.draftLabel")}
              value={draftPrompt}
              onChange={(e) => setDraftPrompt(e.target.value)}
              placeholder={t("campaign.draftPlaceholder")}
            />
            <Button
              variant="secondary"
              size="sm"
              iconStart={<Sparkles size={14} />}
              onClick={() => void draftPersona()}
              disabled={!draftPrompt.trim() || generate.isPending}
            >
              {generate.isPending ? t("campaign.draftPending") : t("campaign.draftGenerate")}
            </Button>
            {draftError && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                {t("campaign.draftError", { message: draftError })}
              </span>
            )}
            <span className="gx-field__hint gx-editor__draft-hint">{t("campaign.draftHint")}</span>
          </div>
        )}
      </div>

      <div className="gx-editor__voice">
        <Combobox
          label={t("campaign.voiceLabel")}
          options={voiceOpts}
          value={voice || undefined}
          onValueChange={setVoice}
          placeholder={t("campaign.voicePlaceholder")}
          searchPlaceholder={t("campaign.voiceSearch")}
          emptyText={t("campaign.voiceEmpty")}
        />
        <Button
          variant="secondary"
          size="sm"
          iconStart={<Volume2 size={14} />}
          onClick={() => void playPreview()}
          disabled={!voice || preview.isPending}
        >
          {t("campaign.voicePreview")}
        </Button>
        {previewError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {t("campaign.voicePreviewError", { message: previewError })}
          </span>
        )}
      </div>

      {/* Tool grants and the addressing switch are power-user territory: the
          everyday fields (name, title, personality, voice) stay in view, and
          these fold into one closed-by-default card at the end of the form.
          The body unmounts while closed, so the grants query only fires once a
          GM actually opens it. */}
      <AdvancedCard title={t("campaign.advancedAgentTitle")} hint={t("campaign.advancedAgentHint")}>
        <ToolGrants agentId={agent.id} />

        <div className="gx-editor__switch">
          <Switch
            label={t("campaign.addressOnlyLabel")}
            checked={butler ? true : addressOnly}
            onCheckedChange={setAddressOnly}
            disabled={butler}
          />
          <span className="gx-field__hint">
            {butler ? t("campaign.addressOnlyButlerHint") : t("campaign.addressOnlyNpcHint")}
          </span>
        </div>
      </AdvancedCard>

      <div className="gx-editor__actions">
        <Button variant="primary" onClick={save} disabled={update.isPending}>
          {update.isPending ? t("common.saving") : t("common.saveChanges")}
        </Button>
        {onDelete && (
          <Button
            variant="danger"
            iconStart={<Trash2 size={14} />}
            onClick={onDelete}
            disabled={deleting}
          >
            {t("campaign.deleteNpc")}
          </Button>
        )}
        {/* Deterministic, accessible save cue — independent of the toast portal so
            the screen test (rendered without the shell's <Toaster>) can assert it. */}
        <span className="gx-editor__status" aria-live="polite">
          {update.isError ? (
            <span className="gx-editor__status--error" role="alert">
              {t("common.couldntSave", { message: update.error.message })}
            </span>
          ) : update.isSuccess ? (
            t("campaign.savedStatus")
          ) : (
            ""
          )}
        </span>
      </div>
    </Card>
  );
}

// ToolGrants renders the per-Agent Tool grant toggles (#117): one row per
// available built-in Tool with its current grant state, backed by
// CampaignService.ListToolGrants. Toggling invalidates the grants query so the
// list re-reads the persisted state (AC2). The available Tools are whatever the
// server's built-in Registry exposes (dice today, ADR-0028); the LLM is only ever
// shown granted Tools (ADR-0029), and a change hydrates into the NEXT session.
function ToolGrants({ agentId }: { agentId: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data, status } = useQuery(CampaignService.method.listToolGrants, { agentId });
  const grants = data?.grants ?? [];

  const invalidateGrants = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listToolGrants,
        cardinality: "finite",
      }),
    });

  return (
    <div className="gx-editor__tools">
      <span className="gx-field__label">{t("campaign.toolsLabel")}</span>
      <span className="gx-field__hint">{t("campaign.toolsHint")}</span>
      {status === "pending" ? (
        <div className="gx-skeleton" data-testid="tools-loading" />
      ) : grants.length === 0 ? (
        <span className="gx-field__hint">{t("campaign.toolsNone")}</span>
      ) : (
        grants.map((g) => <ToolRow key={g.toolName} agentId={agentId} grant={g} onChanged={invalidateGrants} />)
      )}
    </div>
  );
}

// ToolRow is one Tool's grant toggle plus, for a Tool that supports a per-grant
// scope (ADR-0029), an inline scope editor. dice supports no scope, so only the
// on/off Switch renders for it; a scope-supporting Tool also exposes a raw-JSON
// scope field that round-trips through UpdateToolGrant's config. The Switch is
// disabled while its mutation is in flight so a grant can't be double-submitted.
function ToolRow({
  agentId,
  grant,
  onChanged,
}: {
  agentId: string;
  grant: ToolGrant;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const [scope, setScope] = useState(grant.config);

  const update = useMutation(CampaignService.method.updateToolGrant, {
    onSuccess: () => onChanged(),
    onError: (err) =>
      toast.error(t("campaign.toolUpdateError", { tool: grant.toolName, message: err.message })),
  });

  // The grant Switch never carries the local scope draft (#215): turning a grant
  // ON creates a FRESH grant with no scope (empty config → SQL NULL), turning it
  // OFF deletes the row. Only "Save scope" persists config. Resetting the draft to
  // the server value on every toggle stops an unsaved edit from silently
  // persisting and stops an off→on from resurrecting the pre-revoke scope.
  const toggle = (granted: boolean) => {
    setScope(grant.config);
    update.mutate({ agentId, toolName: grant.toolName, granted });
  };
  const saveScope = () =>
    update.mutate({ agentId, toolName: grant.toolName, granted: true, config: scope });

  return (
    <div className="gx-editor__tool">
      <div className="gx-editor__tool-head">
        <Switch
          label={grant.toolName}
          checked={grant.granted}
          disabled={update.isPending}
          onCheckedChange={toggle}
        />
        {grant.description && <span className="gx-field__hint">{grant.description}</span>}
      </div>
      {/* Scope editor only for Tools that support one AND are granted; dice does
          not, so it renders no scope editor (#117). */}
      {grant.supportsScope && grant.granted && (
        <div className="gx-editor__tool-scope">
          {/* toolName is a wire value and stays untranslated inside the label. */}
          <Input
            label={t("campaign.toolScopeLabel", { tool: grant.toolName })}
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            placeholder='{"scope":"self"}'
          />
          <Button variant="secondary" size="sm" onClick={saveScope} disabled={update.isPending}>
            {t("campaign.toolScopeSave")}
          </Button>
        </div>
      )}
    </div>
  );
}
