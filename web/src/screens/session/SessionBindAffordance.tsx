import { useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { UserPlus } from "lucide-react";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { PlayerBindForm } from "@/components/PlayerBindForm";
import type { PlayerBindFields } from "@/components/PlayerBindForm";

// NEW_TARGET is the sentinel Character-select value for "create a new Character"
// (Radix Select forbids an empty item value).
const NEW_TARGET = "__new__";

// SessionBindAffordance is the Session screen's in-flight "unmapped player" bind
// affordance (#279): mid-session the GM can create a Character or reassign an
// existing one to a Discord User WITHOUT leaving (or restarting) the Voice
// Session. It reuses the shared PlayerBindForm and the same CampaignService bind
// RPCs the Players panel uses, so the binding is a plain Character-row write — no
// SessionService call, no restart. With #281 the relay picks the change up on the
// speaker's next Line; this component's job stops at the RPC round-trip.

export function SessionBindAffordance() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  // targetId is the existing Character being reassigned, or NEW_TARGET for a new
  // one (Radix Select forbids an empty-string item value). The form is keyed on it
  // so switching target reseeds the fields.
  const [targetId, setTargetId] = useState(NEW_TARGET);

  // Soft reads: retry:false + empty fallbacks so a degraded call never blocks the
  // affordance (the picker just falls back to free-text).
  const charactersQuery = useQuery(CampaignService.method.listCharacters, {}, { retry: false });
  const characters = useMemo(() => charactersQuery.data?.characters ?? [], [charactersQuery.data]);
  const membersQuery = useQuery(CampaignService.method.listDiscordVoiceMembers, {}, { retry: false });
  const members = useMemo(() => membersQuery.data?.members ?? [], [membersQuery.data]);

  const target = characters.find((c) => c.id === targetId) ?? null;

  const invalidateCharacters = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCharacters,
        cardinality: "finite",
      }),
    });

  const close = () => {
    setOpen(false);
    setTargetId(NEW_TARGET);
  };

  const createCharacter = useMutation(CampaignService.method.createCharacter, {
    onSuccess: () => {
      void invalidateCharacters();
      toast.success("Bound the player — their next line is named.");
      close();
    },
    onError: (err: Error) => toast.error(`Couldn't bind the player: ${err.message}`),
  });
  const updateCharacter = useMutation(CampaignService.method.updateCharacter, {
    onSuccess: () => {
      void invalidateCharacters();
      toast.success("Reassigned the Character — their next line is named.");
      close();
    },
    onError: (err: Error) => toast.error(`Couldn't reassign the Character: ${err.message}`),
  });

  const pending = createCharacter.isPending || updateCharacter.isPending;

  const submit = (fields: PlayerBindFields) => {
    if (target) {
      updateCharacter.mutate({ id: target.id, ...fields });
    } else {
      createCharacter.mutate(fields);
    }
  };

  if (!open) {
    return (
      <div className="gx-session__bind">
        <Button
          variant="secondary"
          size="sm"
          iconStart={<UserPlus size={14} />}
          onClick={() => setOpen(true)}
          data-testid="bind-player-open"
        >
          Bind a player
        </Button>
      </div>
    );
  }

  return (
    <Card accent className="gx-session__bind-form">
      <span className="gx-overline">Bind a player</span>
      {characters.length > 0 && (
        <Select
          label="Character"
          value={targetId}
          onValueChange={setTargetId}
          options={[
            { value: NEW_TARGET, label: "New Character" },
            ...characters.map((c) => ({ value: c.id, label: c.name })),
          ]}
        />
      )}
      <PlayerBindForm
        key={targetId}
        initial={target ? { name: target.name, aliases: target.aliases, discordUserId: target.discordUserId } : undefined}
        members={members}
        submitLabel={target ? "Reassign" : "Create & bind"}
        pending={pending}
        error={
          createCharacter.isError
            ? `Couldn't bind: ${createCharacter.error.message}`
            : updateCharacter.isError
              ? `Couldn't reassign: ${updateCharacter.error.message}`
              : null
        }
        onSubmit={submit}
        onCancel={close}
      />
    </Card>
  );
}
