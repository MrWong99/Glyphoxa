import { useMemo, useState } from "react";
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
import { playAudioBlob } from "@/lib/audio";
import { KnowledgePanel } from "./KnowledgePanel";
import { PlayersPanel } from "./PlayersPanel";
import { ProposalsPanel } from "./ProposalsPanel";

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

export function Campaign() {
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
  const [view, setView] = useState<"cast" | "knowledge" | "players" | "proposals">("cast");

  const invalidateRoster = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.getCampaignRoster,
        cardinality: "finite",
      }),
    });

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
          <h1>{campaign?.name ?? "Campaign"}</h1>
          <div className="gx-seg" role="tablist" aria-label="Campaign view">
            <button
              type="button"
              role="tab"
              aria-selected={view === "cast"}
              data-active={view === "cast" ? "true" : undefined}
              onClick={() => setView("cast")}
            >
              Cast
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "knowledge"}
              data-active={view === "knowledge" ? "true" : undefined}
              onClick={() => setView("knowledge")}
            >
              Knowledge
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "players"}
              data-active={view === "players" ? "true" : undefined}
              onClick={() => setView("players")}
            >
              Players
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "proposals"}
              data-active={view === "proposals" ? "true" : undefined}
              onClick={() => setView("proposals")}
            >
              Proposals
            </button>
          </div>
        </div>
        <div className="gx-campaign-screen__sub">
          {campaign?.system && <span className="gx-campaign-screen__system">{campaign.system}</span>}
          <span className="gx-campaign-screen__lede">
            {view === "knowledge"
              ? "What the world knows. Public entries prime your NPCs; GM-private ones stay yours."
              : view === "players"
                ? "Bind each Discord User to their Character so the transcript names their voice."
                : view === "proposals"
                  ? "What your NPCs want to remember. Approve to make it canon, or reject."
                  : "One Butler is required; add as many NPCs as your table needs."}
          </span>
        </div>
      </header>

      {view === "knowledge" ? (
        <KnowledgePanel />
      ) : view === "players" ? (
        <PlayersPanel />
      ) : view === "proposals" ? (
        <ProposalsPanel />
      ) : status === "pending" ? (
        <div className="gx-skeleton" data-testid="roster-loading" />
      ) : status === "error" ? (
        <p className="gx-campaign__error" role="alert">
          Could not load the campaign: {error.message}
        </p>
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
                  <Lock size={11} /> Butler
                </Badge>
              ) : (
                a.addressOnly && (
                  <Badge variant="neutral" size="sm">
                    Address only
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
              createAgent.mutate({ name: "New NPC", title: "", persona: "", voice: "", addressOnly: false })
            }
          >
            <Plus size={15} /> Add NPC
          </button>

          {npcs.length === 0 && (
            <p className="gx-roster__empty">
              No NPCs yet. The Butler can run a session alone, or add your first NPC above.
            </p>
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
  const butler = isButler(agent);
  const [name, setName] = useState(agent.name);
  const [title, setTitle] = useState(agent.title);
  const [persona, setPersona] = useState(agent.persona);
  const [voice, setVoice] = useState(agent.voice);
  const [addressOnly, setAddressOnly] = useState(agent.addressOnly);

  const update = useMutation(CampaignService.method.updateAgent, {
    onSuccess: () => {
      onSaved();
      toast.success(`Saved ${name || agent.name}`);
    },
    onError: (err) => toast.error(`Couldn't save: ${err.message}`),
  });
  const preview = useMutation(VoiceService.method.previewVoice);

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
              <Sparkles size={11} /> Required
            </Badge>
          ) : (
            <Badge variant="neutral" size="sm">
              NPC
            </Badge>
          )}
          <span className="gx-editor__role">{butler ? "Butler · role locked" : "Character NPC"}</span>
        </div>
      </div>

      <div className="gx-editor__grid">
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} />
        <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Role subtitle" />
      </div>

      <div className="gx-field">
        <label className="gx-field__label" htmlFor="gx-persona">
          Persona
        </label>
        <textarea
          id="gx-persona"
          className="gx-input gx-textarea"
          rows={4}
          value={persona}
          onChange={(e) => setPersona(e.target.value)}
        />
        <span className="gx-field__hint">
          Personality, backstory and speech style — injected into the prompt.
        </span>
      </div>

      <div className="gx-editor__voice">
        <Combobox
          label="Voice"
          options={voiceOpts}
          value={voice || undefined}
          onValueChange={setVoice}
          placeholder="Pick a voice…"
          searchPlaceholder="Search voices…"
          emptyText="No matching voices"
        />
        <Button
          variant="secondary"
          size="sm"
          iconStart={<Volume2 size={14} />}
          onClick={() => void playPreview()}
          disabled={!voice || preview.isPending}
        >
          Preview voice
        </Button>
        {previewError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            Couldn't preview: {previewError}
          </span>
        )}
      </div>

      <div className="gx-editor__switch">
        <Switch
          label="Address only — waits to be named"
          checked={butler ? true : addressOnly}
          onCheckedChange={setAddressOnly}
          disabled={butler}
        />
        <span className="gx-field__hint">
          {butler
            ? "The Butler always waits to be named; it never answers ambient table talk."
            : "When on, this NPC only replies when addressed by name."}
        </span>
      </div>

      <ToolGrants agentId={agent.id} />

      <div className="gx-editor__actions">
        <Button variant="primary" onClick={save} disabled={update.isPending}>
          {update.isPending ? "Saving…" : "Save changes"}
        </Button>
        {onDelete && (
          <Button
            variant="danger"
            iconStart={<Trash2 size={14} />}
            onClick={onDelete}
            disabled={deleting}
          >
            Delete NPC
          </Button>
        )}
        {/* Deterministic, accessible save cue — independent of the toast portal so
            the screen test (rendered without the shell's <Toaster>) can assert it. */}
        <span className="gx-editor__status" aria-live="polite">
          {update.isError ? (
            <span className="gx-editor__status--error" role="alert">
              Couldn't save: {update.error.message}
            </span>
          ) : update.isSuccess ? (
            "Saved"
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
      <span className="gx-field__label">Tools</span>
      <span className="gx-field__hint">
        Grant the Tools this agent may use. Changes take effect in the next session.
      </span>
      {status === "pending" ? (
        <div className="gx-skeleton" data-testid="tools-loading" />
      ) : grants.length === 0 ? (
        <span className="gx-field__hint">No tools available.</span>
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
  const [scope, setScope] = useState(grant.config);

  const update = useMutation(CampaignService.method.updateToolGrant, {
    onSuccess: () => onChanged(),
    onError: (err) => toast.error(`Couldn't update ${grant.toolName}: ${err.message}`),
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
          <Input
            label={`${grant.toolName} scope`}
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            placeholder='{"scope":"self"}'
          />
          <Button variant="secondary" size="sm" onClick={saveScope} disabled={update.isPending}>
            Save scope
          </Button>
        </div>
      )}
    </div>
  );
}
