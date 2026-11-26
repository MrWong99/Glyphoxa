import { useEffect, useState } from "react";
import { Plus, Trash2, X } from "lucide-react";

import type { DiscordVoiceMember } from "@gen/glyphoxa/management/v1/management_pb";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { useI18n } from "@/i18n";
import { DiscordUserField } from "./DiscordUserField";

import "./playerBindForm.css";

// PlayerBindForm is the shared Character bind form (#279): name, aliases, and the
// mandatory Discord User binding. It is reused verbatim by the Campaign Players
// panel and the Session-screen in-flight bind affordance, so it holds NO RPC — it
// emits the edited fields and the caller decides create vs update. Because
// discord_user_id is NOT NULL by design (ADR-0003) there is no "unbind": the form
// only ever binds or rebinds (reassign), and delete lives beside it.
//
// The Discord User field (./DiscordUserField.tsx) pairs a live voice-channel
// member picker with a free-text snowflake fallback — digits-only, so a
// mistyped id can never reach storage. When the Bot is offline the member list
// is simply empty and only the free-text path shows.
//
// `takenIds` (optional — the Session affordance does not pass it) names the
// Discord Users that already have a Character: the picker marks and disables
// them, and a typed id that is taken is refused here, with the Character's
// name, instead of by the server after submit.

export type PlayerBindFields = {
  name: string;
  aliases: string[];
  discordUserId: string;
};

// isSnowflake mirrors the server rule (rpc/character.go isSnowflake): a non-empty
// run of decimal digits. An empty value is "not yet entered", handled separately.
function isSnowflake(s: string): boolean {
  return /^\d+$/.test(s);
}

export function PlayerBindForm({
  initial,
  members,
  submitLabel,
  pending = false,
  error = null,
  onSubmit,
  onCancel,
  onDelete,
  onDirtyChange,
  takenIds,
}: {
  initial?: PlayerBindFields;
  members: DiscordVoiceMember[];
  /**
   * Discord user id → the name of the Character already bound to it. The id
   * this form was opened with (`initial.discordUserId`) is never treated as
   * taken: it is this Character's own.
   */
  takenIds?: ReadonlyMap<string, string>;
  submitLabel: string;
  pending?: boolean;
  error?: string | null;
  onSubmit: (fields: PlayerBindFields) => void;
  onCancel?: () => void;
  onDelete?: () => void;
  /** Reports whether the fields differ from `initial` (the saved Character). */
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(initial?.name ?? "");
  const [aliases, setAliases] = useState<string[]>(initial?.aliases ?? []);
  const [aliasDraft, setAliasDraft] = useState("");
  const [discordUserId, setDiscordUserId] = useState(initial?.discordUserId ?? "");

  // Who already holds a Discord user id — never this form's own.
  const takenBy = (id: string): string | undefined =>
    id !== "" && id !== (initial?.discordUserId ?? "") ? takenIds?.get(id) : undefined;

  const idEmpty = discordUserId.trim() === "";
  const idInvalid = !idEmpty && !isSnowflake(discordUserId.trim());
  // One Discord User plays one Character per campaign: the server refuses a
  // second binding, so say so before the round trip.
  const idTakenBy = idInvalid ? undefined : takenBy(discordUserId.trim());
  const canSubmit = name.trim() !== "" && !idEmpty && !idInvalid && !idTakenBy && !pending;

  const addAlias = () => {
    const a = aliasDraft.trim();
    if (a === "" || aliases.includes(a)) {
      setAliasDraft("");
      return;
    }
    setAliases((xs) => [...xs, a]);
    setAliasDraft("");
  };
  const removeAlias = (a: string) => setAliases((xs) => xs.filter((x) => x !== a));

  const submit = () => {
    if (!canSubmit) return;
    // An alias still sitting in the draft box counts: the GM typed it and
    // pressed Save, and dropping it because they did not press Enter/Add first
    // lost the alias without a word.
    const draft = aliasDraft.trim();
    const allAliases = draft !== "" && !aliases.includes(draft) ? [...aliases, draft] : aliases;
    if (draft !== "") {
      setAliases(allAliases);
      setAliasDraft("");
    }
    onSubmit({ name: name.trim(), aliases: allAliases, discordUserId: discordUserId.trim() });
  };

  // Dirtiness is derived against the live `initial`: the list refetch after a
  // save updates it, so a saved form reads clean again without bookkeeping.
  const isDirty =
    name !== (initial?.name ?? "") ||
    discordUserId !== (initial?.discordUserId ?? "") ||
    aliasDraft.trim() !== "" ||
    aliases.join("\u0000") !== (initial?.aliases ?? []).join("\u0000");
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);
  useEffect(
    () => () => onDirtyChange?.(false), // unmount lifts the guard
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return (
    <div className="gx-playerform">
      <Input
        label={t("components.characterNameLabel")}
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={t("components.characterNamePlaceholder")}
        // The form opens to be typed into — start the caret in its first field,
        // and let Enter submit like the alias/board inputs already do.
        autoFocus
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            submit();
          }
        }}
      />

      {/* Aliases editor — alternate names Address Detection also matches, mirroring
          the Cast panel's alias set. Chips remove on click; the draft input adds on
          Enter or the Add button. */}
      <div className="gx-field">
        <span className="gx-field__label">{t("components.aliasesLabel")}</span>
        {aliases.length > 0 && (
          <div className="gx-playerform__aliases">
            {aliases.map((a) => (
              <Badge key={a} variant="neutral" size="sm">
                {a}
                <button
                  type="button"
                  className="gx-playerform__alias-remove"
                  aria-label={t("components.removeAlias", { alias: a })}
                  onClick={() => removeAlias(a)}
                >
                  <X size={11} />
                </button>
              </Badge>
            ))}
          </div>
        )}
        <div className="gx-playerform__alias-add">
          <Input
            aria-label={t("components.addAliasLabel")}
            value={aliasDraft}
            onChange={(e) => setAliasDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addAlias();
              }
            }}
            placeholder={t("components.addAliasPlaceholder")}
          />
          <Button variant="secondary" size="sm" iconStart={<Plus size={13} />} onClick={addAlias} disabled={aliasDraft.trim() === ""}>
            {t("components.add")}
          </Button>
        </div>
      </div>

      {/* Discord User binding — mandatory (ADR-0003). The field pairs the
          voice-channel member picker with the always-present free-text
          fallback (digits-only); it is shared with the Sharing view. */}
      <DiscordUserField
        value={discordUserId}
        onChange={setDiscordUserId}
        members={members}
        error={
          idInvalid
            ? t("components.discordUserIdDigitsOnly")
            : idTakenBy
              ? t("players.idTaken", { name: idTakenBy })
              : null
        }
        unavailable={(id) => {
          const holder = takenBy(id);
          return holder === undefined ? undefined : t("players.memberTaken", { name: holder });
        }}
        onEnter={submit}
      />

      <div className="gx-playerform__actions">
        <Button variant="primary" onClick={submit} disabled={!canSubmit}>
          {pending ? t("common.saving") : submitLabel}
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={pending}>
            {t("common.cancel")}
          </Button>
        )}
        {onDelete && (
          <Button variant="danger" iconStart={<Trash2 size={14} />} onClick={onDelete} disabled={pending}>
            {t("common.delete")}
          </Button>
        )}
        {error && (
          <span className="gx-playerform__error" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
