import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check as CheckIcon, RotateCcw, Sparkles, Trash2 } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, ToolGrant } from "@gen/glyphoxa/management/v1/management_pb";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { Alert } from "@/components/ui/Alert";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { ChipInput } from "@/components/ui/ChipInput";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Input } from "@/components/ui/Input";
import { Skeleton } from "@/components/ui/Skeleton";
import { Switch } from "@/components/ui/Switch";
import { Textarea } from "@/components/ui/Textarea";
import { Tooltip } from "@/components/ui/Tooltip";
import { useI18n, type MessageKey, type TFunc } from "@/i18n";
import { apiErrorMessage, errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import type { Check, RosterEntry } from "./rosterPrep";
import { VoiceField } from "./VoiceField";
import type { VoicesQuery } from "./VoicePicker";

import "./cast.css";

// AgentEditor edits one roster member. It holds the editable fields in local
// state (seeded from the agent) and saves them via UpdateAgent. For the Butler
// the role is locked, it cannot be deleted, and Address-Only is forced on with a
// disabled switch (ADR-0009 / ADR-0024).
//
// Saving is explicit and its state is always on screen: the action bar sticks
// to the bottom of the viewport, says "Unsaved changes" the moment a field
// differs from what is stored, keeps Save disabled while there is nothing to
// save, offers Revert, and answers Ctrl/Cmd+S. "Saved" shows only while the
// form really is what the server holds.
//
// It was the bottom half of CastPanel.tsx until the cast studio split it out.

function isButler(a: Agent): boolean {
  return a.role === "butler";
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

// The platform's save chord, for the tooltip: ⌘S where the Command key exists.
function isApplePlatform(): boolean {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.platform ?? "");
}

export function AgentEditor({
  agent,
  voicesQuery,
  campaignLanguage,
  prep,
  onSaved,
  onDelete,
  deleting,
  onDirtyChange,
  autoDraft,
  onAutoDraftStarted,
  onOpenNode,
  onOpenWiki,
}: {
  agent: Agent;
  voicesQuery: VoicesQuery;
  /** The Active Campaign's language, for the voice picker's language chip. */
  campaignLanguage?: string;
  /** This agent's readiness, as the roster and the Readiness view grade it. */
  prep?: RosterEntry;
  onSaved: () => void;
  onDelete?: () => void;
  deleting: boolean;
  /** Reports whether the local fields differ from the persisted agent. */
  onDirtyChange?: (dirty: boolean) => void;
  /**
   * A concept from the New NPC dialog: the editor drafts a personality from it
   * once, right after mounting, and shows the result as an unsaved draft.
   */
  autoDraft?: string;
  /** Called when that draft has been started, so it is never started twice. */
  onAutoDraftStarted?: () => void;
  /** Opens one World-wiki entry (the fix for the wiki-side readiness checks). */
  onOpenNode: (nodeID: string) => void;
  /** Opens the World wiki, when there is no entry to open yet. */
  onOpenWiki: () => void;
}) {
  const { t } = useI18n();
  const butler = isButler(agent);

  // What the form is compared against. Normally the agent prop; between a
  // successful save and the roster re-read that follows it, the agent the
  // save RETURNED — otherwise the form would read "Unsaved changes" for that
  // moment, about edits the server has just confirmed. The override is tied to
  // the prop it was saved over, so the re-read retires it by itself.
  const [saved, setSaved] = useState<{ over: Agent; agent: Agent } | null>(null);
  const base = saved && saved.over === agent ? saved.agent : agent;

  const [name, setName] = useState(agent.name);
  const [title, setTitle] = useState(agent.title);
  const [persona, setPersona] = useState(agent.persona);
  const [voice, setVoice] = useState(agent.voice);
  const [addressOnly, setAddressOnly] = useState(agent.addressOnly);
  const [aliases, setAliases] = useState<string[]>(() => [...agent.aliases]);
  // Text typed into the alias field but not yet turned into a chip. It counts
  // as an edit, and Save takes it along: Ctrl+S must not lose a nickname just
  // because the GM never left the field.
  const [aliasDraft, setAliasDraft] = useState("");
  const [aliasFieldEpoch, setAliasFieldEpoch] = useState(0);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const personaRef = useRef<HTMLTextAreaElement>(null);

  const pendingAlias = aliasDraft.trim();
  const hasPendingAlias = pendingAlias !== "" && !aliases.includes(pendingAlias);
  const nameMissing = name.trim() === "";

  // Dirtiness is derived, not tracked: the roster refetch after a save updates
  // the agent prop, so a saved editor reads clean again without bookkeeping.
  const isDirty =
    name !== base.name ||
    title !== base.title ||
    persona !== base.persona ||
    voice !== base.voice ||
    (!butler && addressOnly !== base.addressOnly) ||
    !sameList(aliases, base.aliases) ||
    hasPendingAlias;

  const update = useMutation(CampaignService.method.updateAgent, {
    onSuccess: (res) => {
      if (res.agent) setSaved({ over: agent, agent: res.agent });
      onSaved();
    },
  });

  // On-demand persona drafting (#479): runs when the GM presses Generate, or
  // once for the concept typed into the New NPC dialog. The draft lands in the
  // local persona field for review — nothing is saved until the GM presses
  // "Save changes". Failures render an inline cue like the voice-preview one.
  const [draftPrompt, setDraftPrompt] = useState(autoDraft ?? "");
  const [draftError, setDraftError] = useState<string | null>(null);
  const [draftingConcept, setDraftingConcept] = useState(false);
  const generate = useMutation(CampaignService.method.generatePersona);
  const draftPersona = async (prompt: string) => {
    setDraftError(null);
    try {
      // The live form name/title ride along so the draft addresses what the GM
      // has typed, not the stored cast entry (#480).
      const res = await generate.mutateAsync({ agentId: agent.id, prompt, name, title });
      setPersona(res.persona);
    } catch (err) {
      setDraftError(apiErrorMessage(err, t));
    } finally {
      setDraftingConcept(false);
    }
  };
  const autoStarted = useRef(false);
  useEffect(() => {
    if (!autoDraft || autoStarted.current) return;
    autoStarted.current = true;
    setDraftingConcept(true);
    onAutoDraftStarted?.();
    void draftPersona(autoDraft);
    // Once, for the concept the editor was mounted with.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoDraft]);

  // A draft still being written is as much "work that would be lost" as typed
  // text: leaving now would drop it when it arrives, so it holds the guard too.
  const guarded = isDirty || generate.isPending;
  useEffect(() => {
    onDirtyChange?.(guarded);
  }, [guarded, onDirtyChange]);
  useEffect(
    () => () => onDirtyChange?.(false), // unmount lifts the guard
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const canSave = isDirty && !nameMissing && !update.isPending;
  const save = () => {
    if (!canSave) return;
    // The server trims the name; send (and show) what it will store, so the
    // form is not left "dirty" over a trailing space.
    const nextName = name.trim();
    const nextAliases = hasPendingAlias ? [...aliases, pendingAlias] : aliases;
    if (nextName !== name) setName(nextName);
    if (hasPendingAlias) setAliases(nextAliases);
    update.mutate({
      id: agent.id,
      name: nextName,
      title,
      persona,
      voice,
      // The Butler is always Address-Only; the server enforces this regardless.
      addressOnly: butler ? true : addressOnly,
      aliases: nextAliases,
    });
  };

  const revert = () => {
    setName(base.name);
    setTitle(base.title);
    setPersona(base.persona);
    setVoice(base.voice);
    setAddressOnly(base.addressOnly);
    setAliases([...base.aliases]);
    // The half-typed alias lives inside the ChipInput; a new key empties it.
    setAliasDraft("");
    setAliasFieldEpoch((n) => n + 1);
    update.reset();
  };

  // Ctrl/Cmd+S saves from anywhere on the Cast view — the GM who has just
  // typed a line does not go looking for the button. Not while a dialog is
  // open (the voice picker, a confirmation): the chord belongs to this form,
  // not to whatever is layered over it. The browser's own "save page" is
  // suppressed either way; it has nothing useful to offer here.
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  });
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.key.toLowerCase() !== "s") return;
      const target = event.target instanceof Element ? event.target : null;
      if (target?.closest('[role="dialog"], [role="alertdialog"]')) return;
      event.preventDefault();
      saveRef.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // The readiness callout: what still stands between this NPC and a session.
  // Personality and voice are read from the FORM, so an item disappears as the
  // GM fixes it (the roster marker follows on save); the wiki-side checks can
  // only be fixed in the wiki, so they are links there.
  const missing: Check[] =
    prep && !prep.exempt
      ? prep.checks.filter((c) => {
          if (c.ok) return false;
          if (c.key === "persona") return persona.trim() === "";
          if (c.key === "voice") return voice === "";
          return true;
        })
      : [];
  const trimmed = prep?.readiness?.truncated ? prep.readiness : null;
  const jump = (c: Check) => {
    if (c.key === "persona") personaRef.current?.focus();
    else if (c.key === "voice") setPickerOpen(true);
    else if (c.fix === "entry" && prep?.node) onOpenNode(prep.node.id);
    else if (prep?.linkCandidate) onOpenNode(prep.linkCandidate.id);
    else onOpenWiki();
  };

  const shortcut = isApplePlatform() ? t("cast.saveShortcutMac") : t("cast.saveShortcut");

  return (
    <Card accent className="gx-editor gx-cast-editor">
      {(missing.length > 0 || trimmed) && (
        // role=note: part of the form's standing content, read in place — not
        // an alert that interrupts every time an NPC is selected.
        <Alert
          tone="warning"
          role="note"
          className="gx-cast-callout"
          title={
            missing.length > 0
              ? t("cast.readinessTitle", { name: agent.name })
              : t("cast.trimmed", { chars: trimmed?.chars ?? 0, maxChars: trimmed?.maxChars ?? 0 })
          }
        >
          {missing.length > 0 && (
            <ul className="gx-cast-callout__list" aria-label={t("cast.readinessListAria")}>
              {missing.map((c) => (
                <li key={c.key}>
                  <button type="button" className="gx-cast-link" onClick={() => jump(c)}>
                    {t(c.missingKey)}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {trimmed && (
            <p className="gx-cast-callout__trimmed">
              {missing.length > 0 && (
                <strong>{t("cast.trimmed", { chars: trimmed.chars, maxChars: trimmed.maxChars })}. </strong>
              )}
              {t("cast.trimmedHint")}
            </p>
          )}
        </Alert>
      )}

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
        <Input
          label={t("campaign.nameLabel")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={nameMissing ? t("cast.nameRequired") : null}
          aria-required="true"
          autoComplete="off"
        />
        <Input
          label={t("campaign.titleLabel")}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder={t("campaign.titlePlaceholder")}
          autoComplete="off"
        />
      </div>

      {/* Extra address terms (Agent.aliases) for Address Detection: what the
          table actually calls this character, and what speech-to-text makes of
          the name. Shown for the Butler too — it is addressed by name more
          than anyone. */}
      <ChipInput
        key={aliasFieldEpoch}
        label={t("cast.aliasesLabel")}
        hint={t("cast.aliasesHint")}
        placeholder={t("cast.aliasesPlaceholder")}
        values={aliases}
        onChange={setAliases}
        onDraftChange={setAliasDraft}
      />

      {/* When this character answers sits with who they are, not behind the
          advanced card: it changes how the NPC behaves at the table. */}
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

      <div className="gx-cast-editor__persona">
        {/* The wire field is still `persona`; the GM sees plain language. */}
        <Textarea
          ref={personaRef}
          label={t("campaign.personaLabel")}
          hint={t("campaign.personaHint")}
          rows={4}
          autoGrow
          maxRows={18}
          value={persona}
          onChange={(e) => setPersona(e.target.value)}
          aria-busy={generate.isPending || undefined}
        />
        {draftingConcept && generate.isPending && (
          <span className="gx-cast-editor__drafting" role="status">
            {t("cast.draftFromConcept")}
          </span>
        )}
        {!butler && (
          <div className="gx-editor__draft">
            <Input
              label={t("campaign.draftLabel")}
              value={draftPrompt}
              onChange={(e) => setDraftPrompt(e.target.value)}
              placeholder={t("campaign.draftPlaceholder")}
              autoComplete="off"
            />
            <Button
              variant="secondary"
              size="sm"
              iconStart={<Sparkles size={14} />}
              onClick={() => void draftPersona(draftPrompt)}
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

      <VoiceField
        label={t("campaign.voiceLabel")}
        voicesQuery={voicesQuery}
        value={voice}
        onChange={setVoice}
        campaignLanguage={campaignLanguage}
        pickerOpen={pickerOpen}
        onPickerOpenChange={setPickerOpen}
      />

      {/* Tool grants are power-user territory: the everyday fields stay in
          view, and the grants fold into one closed-by-default card at the end
          of the form. The body unmounts while closed, so the grants query only
          fires once a GM actually opens it. */}
      <AdvancedCard title={t("campaign.advancedAgentTitle")} hint={t("campaign.advancedAgentHint")}>
        <ToolGrants agentId={agent.id} />
      </AdvancedCard>

      <div className="gx-cast-bar" role="group" aria-label={t("cast.saveBarAria")}>
        <Tooltip content={t("cast.saveTooltip", { shortcut })} disabled={!canSave}>
          <Button
            variant="primary"
            onClick={save}
            disabled={!canSave}
            loading={update.isPending}
            aria-keyshortcuts="Control+S Meta+S"
          >
            {t("common.saveChanges")}
          </Button>
        </Tooltip>
        <Button
          variant="ghost"
          iconStart={<RotateCcw size={14} />}
          onClick={revert}
          disabled={!isDirty || update.isPending}
        >
          {t("cast.revert")}
        </Button>
        {/* Deterministic, accessible save state — independent of the toast
            portal. A failed save stays here (the editor is still open on the
            GM's text); "Saved" shows only while the form is what was stored. */}
        <span className="gx-cast-bar__status" aria-live="polite">
          {update.isError ? (
            <span className="gx-editor__status--error" role="alert">
              {t("common.couldntSave", { message: errorMessage(update.error) })}
            </span>
          ) : isDirty ? (
            <span className="gx-cast-bar__unsaved">{t("common.unsavedChanges")}</span>
          ) : update.isSuccess ? (
            <span className="gx-cast-bar__saved">
              <CheckIcon size={14} aria-hidden="true" />
              {t("common.saved")}
            </span>
          ) : null}
        </span>
        {onDelete && (
          <>
            <Button
              variant="danger"
              className="gx-cast-bar__delete"
              iconStart={<Trash2 size={14} />}
              onClick={() => setConfirmDelete(true)}
              disabled={deleting}
            >
              {t("campaign.deleteNpc")}
            </Button>
            {/* Deleting an NPC is the destructive act every other surface gates
                behind a ConfirmDialog (players, maps, campaigns) — one stray
                click must not remove a cast member. */}
            <ConfirmDialog
              open={confirmDelete}
              onOpenChange={setConfirmDelete}
              title={t("campaign.deleteNpcTitle", { name: agent.name })}
              description={t("campaign.deleteNpcDesc")}
              confirmLabel={t("campaign.deleteNpc")}
              onConfirm={() => {
                setConfirmDelete(false);
                onDelete();
              }}
            />
          </>
        )}
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
//
// The grants are NOT part of the form above: a switch here writes at once, and
// the label says so, because everything else on this card waits for Save.
function ToolGrants({ agentId }: { agentId: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data, status } = useQuery(CampaignService.method.listToolGrants, { agentId });
  const grants = data?.grants ?? [];

  const invalidateGrants = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listToolGrants);

  return (
    <div className="gx-editor__tools">
      <span className="gx-field__label">{t("campaign.toolsLabel")}</span>
      <span className="gx-field__hint">{t("campaign.toolsHint")}</span>
      {status === "pending" ? (
        <Skeleton lines={2} />
      ) : status === "error" ? (
        // A failed read is not an empty registry — "No tools available" on a
        // transport error would tell the GM the wrong story.
        <span className="gx-field__hint" role="alert">
          {t("campaign.toolsLoadError")}
        </span>
      ) : grants.length === 0 ? (
        <span className="gx-field__hint">{t("campaign.toolsNone")}</span>
      ) : (
        grants.map((g) => <ToolRow key={g.toolName} agentId={agentId} grant={g} onChanged={invalidateGrants} />)
      )}
    </div>
  );
}

// TOOL_LABEL_KEYS maps a voice Tool's wire name (pkg/tool Name()) onto a
// friendly label; a Tool this map doesn't know still renders, de-snake_cased,
// rather than leaking underscores (the planning chat does the same, chat.ts).
const TOOL_LABEL_KEYS: Record<string, MessageKey> = {
  dice: "campaign.toolDice",
  recap: "campaign.toolRecap",
  kg_query: "campaign.toolKgQuery",
  transcript_search: "campaign.toolTranscriptSearch",
  remember_knowledge: "campaign.toolRememberKnowledge",
  locate_entity: "campaign.toolLocateEntity",
  whats_nearby: "campaign.toolWhatsNearby",
};

function toolLabel(t: TFunc, toolName: string): string {
  const key = TOOL_LABEL_KEYS[toolName];
  return key ? t(key) : toolName.replace(/_/g, " ");
}

// ToolRow is one Tool's grant toggle plus, for a Tool that supports a per-grant
// scope (ADR-0029), an inline scope editor. dice supports no scope, so only the
// on/off Switch renders for it; a scope-supporting Tool also exposes a raw-JSON
// scope field that round-trips through UpdateToolGrant's config, tucked behind a
// closed "Advanced" disclosure since raw JSON is power-user territory. The
// Switch is disabled while its mutation is in flight so a grant can't be
// double-submitted.
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
  const label = toolLabel(t, grant.toolName);

  const update = useMutation(CampaignService.method.updateToolGrant, {
    onSuccess: () => onChanged(),
    onError: (err) =>
      toast.error(t("campaign.toolUpdateError", { tool: label, message: errorMessage(err) })),
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
          label={label}
          checked={grant.granted}
          disabled={update.isPending}
          onCheckedChange={toggle}
        />
        {grant.description && <span className="gx-field__hint">{grant.description}</span>}
      </div>
      {/* Scope editor only for Tools that support one AND are granted; dice does
          not, so it renders no scope editor (#117). */}
      {grant.supportsScope && grant.granted && (
        <details className="gx-editor__tool-advanced">
          <summary>{t("campaign.toolScopeAdvanced")}</summary>
          <div className="gx-editor__tool-scope">
            <Input
              label={t("campaign.toolScopeLabel", { tool: label })}
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder='{"scope":"self"}'
            />
            <Button variant="secondary" size="sm" onClick={saveScope} disabled={update.isPending}>
              {t("campaign.toolScopeSave")}
            </Button>
          </div>
        </details>
      )}
    </div>
  );
}
