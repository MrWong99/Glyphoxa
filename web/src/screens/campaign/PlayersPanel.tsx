import { useId, useMemo, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Plus, UserPlus, Users } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Character, DiscordVoiceMember } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { Skeleton } from "@/components/ui/Skeleton";
import { Tooltip } from "@/components/ui/Tooltip";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { PlayerBindForm } from "@/components/PlayerBindForm";
import type { PlayerBindFields } from "@/components/PlayerBindForm";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";

import "./players.css";

// PlayersPanel (#279) is the Campaign screen's Players view: the campaign's
// Player Characters, each bound to a Discord User, with a bind form to create a
// Character or reassign an existing one. Since discord_user_id is NOT NULL
// (ADR-0003) there is no unbind — removing a mapping is a reassign (edit the
// Discord User) or a delete. The member picker reads the Bot's live
// voice-channel occupants (ListDiscordVoiceMembers); when the Bot is offline the
// list is empty and the form falls back to free-text entry.
//
// A row leads with the CHARACTER — its name and its initial — because that is
// what the GM knows a player by. Who plays it comes second: the member's Discord
// name while they are in the voice channel, and otherwise a masked id ("Discord
// user …1234 · not in voice"). The list used to print the raw 18-digit
// snowflake there, and draw the avatar from it, so between sessions — which is
// when a GM prepares — every row was a number with a "1" in a circle. (Naming a
// member who is NOT in voice needs a server-side lookup that is not on the wire
// yet.)

/** The end of a Discord user id: enough to tell two players apart, short enough to read. */
function maskedID(id: string): string {
  return id.length > 4 ? `…${id.slice(-4)}` : id;
}

export function PlayersPanel() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const unlinkedHeadingID = useId();
  const listQuery = useQuery(CampaignService.method.listCharacters, {});
  const characters = useMemo(() => listQuery.data?.characters ?? [], [listQuery.data]);

  // Live voice-channel members for the picker (#279). Soft: retry:false and an
  // empty fallback so a bot-offline / keyless deployment degrades to free-text.
  const membersQuery = useQuery(CampaignService.method.listDiscordVoiceMembers, {}, { retry: false });
  const members = useMemo(() => membersQuery.data?.members ?? [], [membersQuery.data]);
  const membersById = useMemo(
    () => new Map(members.map((m) => [m.discordUserId, m])),
    [members],
  );
  // Discord user id → the Character that holds it.
  const boundBy = useMemo(() => new Map(characters.map((c) => [c.discordUserId, c])), [characters]);
  // People in the voice channel right now whom the transcript cannot name yet.
  const unlinked = useMemo(() => members.filter((m) => !boundBy.has(m.discordUserId)), [members, boundBy]);

  // editing is the Character whose editor is open (reassign/delete); creating opens
  // the create form — blank, or prefilled with a voice member. They are mutually
  // exclusive.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [creating, setCreating] = useState<{ memberID: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Character | null>(null);
  const editing = characters.find((c) => c.id === editingId) ?? null;

  const invalidateCharacters = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listCharacters);

  const createCharacter = useMutation(CampaignService.method.createCharacter, {
    onSuccess: (res) => {
      void invalidateCharacters();
      setCreating(null);
      // The new Character's editor must not open wearing the previous one's
      // "Saved" / "Couldn't save" line (see show below).
      updateCharacter.reset();
      if (res.character) setEditingId(res.character.id);
    },
  });
  const updateCharacter = useMutation(CampaignService.method.updateCharacter, {
    onSuccess: () => void invalidateCharacters(),
  });
  const deleteCharacter = useMutation(CampaignService.method.deleteCharacter, {
    onSuccess: () => {
      setEditingId(null);
      void invalidateCharacters();
    },
    // The confirm dialog closes on confirm, so a rejected delete needs its own
    // voice (ADR-0017: sonner) — silence here reads as a completed delete.
    onError: (err) =>
      toast.error(t("campaign.couldntDeletePlayer", { message: errorMessage(err) })),
  });

  // Unsaved typing in the bind form is guarded (W10): every move below remounts
  // the form and would silently drop it.
  const edits = useDirtyGuard();
  // The create/update mutations are shared by every Character, so their settled
  // status must not outlive the form it belongs to: without the resets,
  // selecting Kira after a failed save on Bram showed Bram's error (or his
  // "Saved") on Kira, and a reopened create form opened on its last failure.
  const show = (next: { creating: { memberID: string } | null; editingId: string | null }) =>
    edits.guard(() => {
      createCharacter.reset();
      updateCharacter.reset();
      setCreating(next.creating);
      setEditingId(next.editingId);
    });
  const startCreate = (member?: DiscordVoiceMember) =>
    show({ creating: { memberID: member?.discordUserId ?? "" }, editingId: null });
  const selectCharacter = (c: Character) => {
    if (c.id === editingId && !creating) return;
    show({ creating: null, editingId: c.id });
  };

  const submitCreate = (fields: PlayerBindFields) => createCharacter.mutate(fields);
  const submitEdit = (fields: PlayerBindFields) => {
    if (!editing) return;
    updateCharacter.mutate({ id: editing.id, ...fields });
  };

  // What the bind form refuses: every Discord user that already has a
  // Character — except, while editing, the Character on the table.
  const takenIds = useMemo(() => {
    const taken = new Map<string, string>();
    for (const c of characters) if (c.id !== editingId || creating) taken.set(c.discordUserId, c.name);
    return taken;
  }, [characters, editingId, creating]);

  if (!listQuery.data) {
    if (listQuery.isPending) return <Skeleton.Row count={3} label={t("players.loading")} />;
    return (
      <ErrorState
        title={t("players.loadErrorTitle")}
        message={errorMessage(listQuery.error)}
        onRetry={() => void listQuery.refetch()}
        retrying={listQuery.isFetching}
      />
    );
  }

  const unlinkedSection = unlinked.length > 0 && (
    <section className="gx-players__unlinked" aria-labelledby={unlinkedHeadingID}>
      <h2 id={unlinkedHeadingID} className="gx-players__section-title">
        {t("players.unlinkedTitle")}
      </h2>
      <p className="gx-players__section-hint">{t("players.unlinkedHint")}</p>
      <ul className="gx-players__list">
        {unlinked.map((m) => (
          <li key={m.discordUserId} className="gx-players__row gx-players__row--unlinked">
            <Avatar name={m.displayName} src={m.avatarUrl || null} size="sm" status="live" />
            <span className="gx-players__meta">
              <span className="gx-players__name">{m.displayName}</span>
              <span className="gx-players__sub">{t("players.inVoice")}</span>
            </span>
            <Button
              variant="secondary"
              size="sm"
              iconStart={<UserPlus size={13} />}
              aria-label={t("players.createCharacterFor", { name: m.displayName })}
              onClick={() => startCreate(m)}
            >
              {t("players.createCharacter")}
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );

  const createForm = creating && (
    <Card accent className="gx-editor">
      <div className="gx-editor__head">
        <Badge variant="neutral" size="sm">
          <Plus size={11} /> {t("campaign.newPlayerBadge")}
        </Badge>
        <span className="gx-editor__role">
          {membersById.has(creating.memberID)
            ? t("players.newPlayerFor", { name: membersById.get(creating.memberID)?.displayName ?? "" })
            : t("campaign.newPlayerRole")}
        </span>
      </div>
      <PlayerBindForm
        // Keyed on the member: "Create character" for someone else re-seeds the form.
        key={`new:${creating.memberID}`}
        initial={
          creating.memberID !== ""
            ? { name: "", aliases: [], discordUserId: creating.memberID }
            : undefined
        }
        members={members}
        takenIds={takenIds}
        submitLabel={t("campaign.createPlayer")}
        pending={createCharacter.isPending}
        error={
          createCharacter.isError
            ? t("campaign.couldntCreate", { message: errorMessage(createCharacter.error) })
            : null
        }
        onSubmit={submitCreate}
        onCancel={() => {
          // Cancel IS the explicit discard — no second question.
          edits.markDirty(false);
          createCharacter.reset();
          setCreating(null);
        }}
        onDirtyChange={edits.markDirty}
      />
    </Card>
  );

  // Nobody yet: say what this view is for and put the one action there, instead
  // of an empty column beside a card that asks the GM to select from it.
  if (characters.length === 0 && !creating) {
    return (
      <div className="gx-players-panel">
        <EmptyState
          icon={<Users size={22} />}
          title={t("players.emptyTitle")}
          description={t("players.emptyDesc")}
          titleAs="h2"
          action={
            <Button variant="primary" iconStart={<UserPlus size={14} />} onClick={() => startCreate()}>
              {t("campaign.addPlayer")}
            </Button>
          }
        />
        {unlinkedSection}
        {edits.dialog}
      </div>
    );
  }

  return (
    <div className="gx-players-panel">
      <div className="gx-roster-layout">
        <div className="gx-players">
          <div className="gx-players__head">
            <span className="gx-overline">{t("campaign.playersCount", { n: characters.length })}</span>
            <Button variant="secondary" size="sm" iconStart={<UserPlus size={14} />} onClick={() => startCreate()}>
              {t("campaign.addPlayer")}
            </Button>
          </div>

          <ul className="gx-players__list">
            {characters.map((c) => {
              const member = membersById.get(c.discordUserId);
              const active = editingId === c.id && !creating;
              return (
                <li key={c.id} className="gx-players__row" data-active={active ? "true" : undefined}>
                  <button
                    type="button"
                    className="gx-players__item"
                    aria-current={active || undefined}
                    onClick={() => selectCharacter(c)}
                  >
                    {/* The CHARACTER's initial, coloured by its name. */}
                    <Avatar name={c.name} size="sm" status={member ? "live" : null} />
                    <span className="gx-players__meta">
                      <span className="gx-players__name">{c.name}</span>
                      <span className="gx-players__sub">
                        <span className="gx-players__who">
                          {member ? member.displayName : t("players.maskedUser", { tail: maskedID(c.discordUserId) })}
                        </span>
                        <span className="gx-players__presence">
                          {" · "}
                          {member ? t("players.inVoice") : t("players.notInVoice")}
                        </span>
                      </span>
                    </span>
                  </button>
                  {/* Outside the row's button: a badge that explains itself has
                      to be reachable on its own, and a focusable thing inside a
                      button is not. A button of its own (the tooltip's
                      trigger), not a focusable span a screen reader announces
                      as bare text. */}
                  {c.linkedUserId !== "" ? (
                    <Tooltip content={t("players.badgeLinkedTip")}>
                      <button type="button" className="gx-players__badge">
                        <Badge variant="gold" size="sm">
                          {t("campaign.badgeLinked")}
                        </Badge>
                      </button>
                    </Tooltip>
                  ) : (
                    !member && (
                      <Tooltip content={t("players.badgeDiscordIdTip")}>
                        <button type="button" className="gx-players__badge">
                          <Badge variant="neutral" size="sm">
                            {t("campaign.badgeDiscordId")}
                          </Badge>
                        </button>
                      </Tooltip>
                    )
                  )}
                </li>
              );
            })}
          </ul>

          {unlinkedSection}
        </div>

        {creating ? (
          createForm
        ) : editing ? (
          <Card accent className="gx-editor">
            <div className="gx-editor__head">
              <Avatar name={editing.name} size="lg" />
              <div className="gx-editor__head-meta">
                <Badge variant="neutral" size="sm">
                  {t("campaign.badgePlayer")}
                </Badge>
                <span className="gx-editor__role">{t("campaign.editPlayerRole")}</span>
              </div>
            </div>
            <PlayerBindForm
              key={editing.id}
              initial={{ name: editing.name, aliases: editing.aliases, discordUserId: editing.discordUserId }}
              members={members}
              takenIds={takenIds}
              submitLabel={t("common.saveChanges")}
              pending={updateCharacter.isPending}
              error={
                updateCharacter.isError
                  ? t("common.couldntSave", { message: errorMessage(updateCharacter.error) })
                  : null
              }
              onSubmit={submitEdit}
              onDelete={() => setConfirmDelete(editing)}
              onDirtyChange={edits.markDirty}
            />
            {updateCharacter.isSuccess && !updateCharacter.isPending && (
              <span className="gx-editor__status" aria-live="polite">
                {t("campaign.savedStatus")}
              </span>
            )}
          </Card>
        ) : (
          <Card className="gx-players__prompt">
            <EmptyState size="inline" icon={<Users size={16} />} title={t("campaign.playersPrompt")} />
          </Card>
        )}
      </div>

      {edits.dialog}

      {confirmDelete && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmDelete(null);
          }}
          title={t("campaign.deletePlayerTitle", { name: confirmDelete.name })}
          description={t("campaign.deletePlayerDesc")}
          confirmLabel={t("campaign.deletePlayerConfirm")}
          onConfirm={() => {
            deleteCharacter.mutate({ id: confirmDelete.id });
            setConfirmDelete(null);
          }}
        />
      )}
    </div>
  );
}
