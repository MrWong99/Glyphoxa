import { useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Plus, UserPlus } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Character, DiscordVoiceMember } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { PlayerBindForm } from "@/components/PlayerBindForm";
import type { PlayerBindFields } from "@/components/PlayerBindForm";

// PlayersPanel (#279) is the Campaign screen's third view: the campaign's Player
// Characters, each bound to a Discord User, with a bind form to create a Character
// or reassign an existing one. Since discord_user_id is NOT NULL (ADR-0003) there
// is no unbind — removing a mapping is a reassign (edit the Discord User) or a
// delete. The member picker reads the Bot's live voice-channel occupants
// (ListDiscordVoiceMembers); when the Bot is offline the list is empty and the
// form falls back to free-text snowflake entry.

// subtitleFor resolves a Character's Discord binding to a display line: the live
// member's name when the Bot can see them, else the raw snowflake (Bot offline).
function subtitleFor(character: Character, byId: Map<string, DiscordVoiceMember>): string {
  return byId.get(character.discordUserId)?.displayName ?? character.discordUserId;
}

export function PlayersPanel() {
  const queryClient = useQueryClient();
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

  // editing is the Character whose editor is open (reassign/delete); creating opens
  // the create form. They are mutually exclusive.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Character | null>(null);
  const editing = characters.find((c) => c.id === editingId) ?? null;

  const invalidateCharacters = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCharacters,
        cardinality: "finite",
      }),
    });

  const createCharacter = useMutation(CampaignService.method.createCharacter, {
    onSuccess: (res) => {
      void invalidateCharacters();
      setCreating(false);
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
  });

  const startCreate = () => {
    setCreating(true);
    setEditingId(null);
  };
  const selectCharacter = (c: Character) => {
    setEditingId(c.id);
    setCreating(false);
  };

  const submitCreate = (fields: PlayerBindFields) => createCharacter.mutate(fields);
  const submitEdit = (fields: PlayerBindFields) => {
    if (!editing) return;
    updateCharacter.mutate({ id: editing.id, ...fields });
  };

  if (listQuery.status === "pending") {
    return <div className="gx-skeleton" data-testid="players-loading" />;
  }
  if (listQuery.status === "error") {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not load players: {listQuery.error.message}
      </p>
    );
  }

  return (
    <div className="gx-roster-layout">
      <div className="gx-players">
        <div className="gx-players__head">
          <span className="gx-overline">Players · {characters.length}</span>
          <Button variant="secondary" size="sm" iconStart={<UserPlus size={14} />} onClick={startCreate}>
            Add player
          </Button>
        </div>

        {characters.map((c) => (
          <button
            key={c.id}
            type="button"
            className="gx-players__item"
            data-active={editingId === c.id ? "true" : undefined}
            onClick={() => selectCharacter(c)}
          >
            <Avatar name={subtitleFor(c, membersById)} src={membersById.get(c.discordUserId)?.avatarUrl || null} size="sm" />
            <span className="gx-players__meta">
              <span className="gx-players__name">{c.name}</span>
              <span className="gx-players__sub">{subtitleFor(c, membersById)}</span>
            </span>
            {c.linkedUserId !== "" ? (
              <Badge variant="gold" size="sm">
                Linked
              </Badge>
            ) : (
              !membersById.has(c.discordUserId) && (
                <Badge variant="neutral" size="sm">
                  Discord ID
                </Badge>
              )
            )}
          </button>
        ))}

        {characters.length === 0 && (
          <p className="gx-players__empty">
            No players yet. Add a Character and bind it to a Discord User so their voice is named.
          </p>
        )}
      </div>

      {creating ? (
        <Card accent className="gx-editor">
          <div className="gx-editor__head">
            <Badge variant="neutral" size="sm">
              <Plus size={11} /> New player
            </Badge>
            <span className="gx-editor__role">Bind a Discord User to a Character</span>
          </div>
          <PlayerBindForm
            members={members}
            submitLabel="Create player"
            pending={createCharacter.isPending}
            error={createCharacter.isError ? `Couldn't create: ${createCharacter.error.message}` : null}
            onSubmit={submitCreate}
            onCancel={() => setCreating(false)}
          />
        </Card>
      ) : editing ? (
        <Card accent className="gx-editor">
          <div className="gx-editor__head">
            <Avatar name={subtitleFor(editing, membersById)} src={membersById.get(editing.discordUserId)?.avatarUrl || null} size="lg" />
            <div className="gx-editor__head-meta">
              <Badge variant="neutral" size="sm">
                Player
              </Badge>
              <span className="gx-editor__role">Reassign the Discord User or delete the Character</span>
            </div>
          </div>
          <PlayerBindForm
            key={editing.id}
            initial={{ name: editing.name, aliases: editing.aliases, discordUserId: editing.discordUserId }}
            members={members}
            submitLabel="Save changes"
            pending={updateCharacter.isPending}
            error={updateCharacter.isError ? `Couldn't save: ${updateCharacter.error.message}` : null}
            onSubmit={submitEdit}
            onDelete={() => setConfirmDelete(editing)}
          />
          {updateCharacter.isSuccess && !updateCharacter.isPending && (
            <span className="gx-editor__status" aria-live="polite">
              Saved
            </span>
          )}
        </Card>
      ) : (
        <Card className="gx-players__prompt">
          <p>Select a player to reassign or delete, or add a new one.</p>
        </Card>
      )}

      {confirmDelete && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmDelete(null);
          }}
          title={`Delete “${confirmDelete.name}”?`}
          description="This removes the Character and its Discord binding. To keep the Player but map them to a different Discord User, reassign instead of deleting."
          confirmLabel="Delete player"
          onConfirm={() => {
            deleteCharacter.mutate({ id: confirmDelete.id });
            setConfirmDelete(null);
          }}
        />
      )}
    </div>
  );
}
