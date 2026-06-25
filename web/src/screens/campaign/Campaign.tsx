import { useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Lock, Plus, Sparkles, Trash2 } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Avatar } from "@/components/ui/Avatar";
import { Select } from "@/components/ui/Select";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";

import "./campaign.css";

// The Campaign screen (#71) backs the design's Butler/NPC editor on the live
// CampaignService roster + CRUD RPCs (ADR-0039). The Butler is required, role-
// locked and undeletable (ADR-0009); its Address-Only switch is forced on and
// disabled (ADR-0024). NPCs are added, edited and deleted; every edit round-trips
// to the DB and the roster re-reads it back after each mutation invalidates the
// query.

// The voice dropdown is a static allowlist placeholder this slice — the live
// ElevenLabs ListVoices wiring lands with the provider RPCs. The selected id is
// persisted to the agent's voice field regardless, so edits round-trip.
const VOICE_OPTIONS = ["rachel", "adam", "antoni", "bella", "elli", "domi"];

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

  // Selection: the chosen agent, defaulting to the first roster member (the
  // Butler) until the operator picks another. Falls back to the Butler when the
  // selected NPC is deleted.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = roster.find((a) => a.id === selectedId) ?? roster[0];

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

  if (status === "pending") {
    return (
      <div className="gx-campaign-screen">
        <h1>Campaign</h1>
        <div className="gx-skeleton" data-testid="roster-loading" />
      </div>
    );
  }
  if (status === "error") {
    return (
      <div className="gx-campaign-screen">
        <h1>Campaign</h1>
        <p className="gx-campaign__error" role="alert">
          Could not load the campaign: {error.message}
        </p>
      </div>
    );
  }

  const campaign = data?.campaign;
  const npcs = roster.filter((a) => !isButler(a));

  return (
    <div className="gx-campaign-screen">
      <header className="gx-campaign-screen__header">
        <h1>{campaign?.name ?? "Campaign"}</h1>
        <div className="gx-campaign-screen__sub">
          {campaign?.system && <span className="gx-campaign-screen__system">{campaign.system}</span>}
          <span className="gx-campaign-screen__lede">
            One Butler is required; add as many NPCs as your table needs.
          </span>
        </div>
      </header>

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
            onSaved={() => void invalidateRoster()}
            onDelete={
              isButler(selected) ? undefined : () => deleteAgent.mutate({ id: selected.id })
            }
            deleting={deleteAgent.isPending}
          />
        )}
      </div>
    </div>
  );
}

// AgentEditor edits one roster member. It holds the editable fields in local
// state (seeded from the agent) and saves them via UpdateAgent. For the Butler
// the role is locked, it cannot be deleted, and Address-Only is forced on with a
// disabled switch (ADR-0009 / ADR-0024).
function AgentEditor({
  agent,
  onSaved,
  onDelete,
  deleting,
}: {
  agent: Agent;
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

  const update = useMutation(CampaignService.method.updateAgent, { onSuccess: onSaved });

  const voiceOpts = useMemo(() => {
    const set = new Set(VOICE_OPTIONS);
    if (voice) set.add(voice);
    return [...set];
  }, [voice]);

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

      <Select label="Voice" options={voiceOpts} value={voice || undefined} onValueChange={setVoice} placeholder="Pick a voice…" />

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

      <div className="gx-editor__actions">
        <Button variant="primary" onClick={save} disabled={update.isPending}>
          Save changes
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
      </div>
    </Card>
  );
}
