import { useMemo, useRef, useState } from "react";
import { Plus, Trash2, Users, X } from "lucide-react";

import type { DiscordVoiceMember } from "@gen/glyphoxa/management/v1/management_pb";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { useI18n } from "@/i18n";

// PlayerBindForm is the shared Character bind form (#279): name, aliases, and the
// mandatory Discord User binding. It is reused verbatim by the Campaign Players
// panel and the Session-screen in-flight bind affordance, so it holds NO RPC — it
// emits the edited fields and the caller decides create vs update. Because
// discord_user_id is NOT NULL by design (ADR-0003) there is no "unbind": the form
// only ever binds or rebinds (reassign), and delete lives beside it.
//
// The Discord User field pairs a live voice-channel member picker (the Bot's
// current voice states, #279) with a free-text snowflake fallback — digits-only,
// so a mistyped id can never reach storage. When the Bot is offline the member
// list is simply empty and only the free-text path shows.

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
}: {
  initial?: PlayerBindFields;
  members: DiscordVoiceMember[];
  submitLabel: string;
  pending?: boolean;
  error?: string | null;
  onSubmit: (fields: PlayerBindFields) => void;
  onCancel?: () => void;
  onDelete?: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(initial?.name ?? "");
  const [aliases, setAliases] = useState<string[]>(initial?.aliases ?? []);
  const [aliasDraft, setAliasDraft] = useState("");
  const [discordUserId, setDiscordUserId] = useState(initial?.discordUserId ?? "");
  const [pickerOpen, setPickerOpen] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);
  usePopoverDismiss(pickerRef, pickerOpen, () => setPickerOpen(false));

  // A bound id that matches a live member shows that member's name beside the
  // field; otherwise the raw snowflake stands in (the Bot may be offline).
  const boundMember = useMemo(
    () => members.find((m) => m.discordUserId === discordUserId),
    [members, discordUserId],
  );

  const idEmpty = discordUserId.trim() === "";
  const idInvalid = !idEmpty && !isSnowflake(discordUserId.trim());
  const canSubmit = name.trim() !== "" && !idEmpty && !idInvalid && !pending;

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

  const pickMember = (m: DiscordVoiceMember) => {
    setDiscordUserId(m.discordUserId);
    setPickerOpen(false);
  };

  const submit = () => {
    if (!canSubmit) return;
    onSubmit({ name: name.trim(), aliases, discordUserId: discordUserId.trim() });
  };

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

      {/* Discord User binding — mandatory (ADR-0003). The picker lists the Bot's
          current voice-channel members; the free-text field is the always-present
          fallback (digits-only). */}
      <div className="gx-field" ref={pickerRef}>
        <span className="gx-field__label">{t("components.discordUserLabel")}</span>
        <div className="gx-playerform__bind">
          <Input
            aria-label={t("components.discordUserIdLabel")}
            value={discordUserId}
            onChange={(e) => setDiscordUserId(e.target.value)}
            // "snowflake" is Discord-internals jargon — the visible copy says
            // "numbers only"; the wire rule (isSnowflake) is unchanged.
            placeholder={t("components.discordUserIdPlaceholder")}
            aria-invalid={idInvalid || undefined}
            inputMode="numeric"
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                submit();
              }
            }}
          />
          {members.length > 0 && (
            <div className="gx-playerform__picker-anchor">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                iconStart={<Users size={13} />}
                onClick={() => setPickerOpen((o) => !o)}
                aria-expanded={pickerOpen}
              >
                {t("components.fromVoice")}
              </Button>
              {pickerOpen && (
                // A labelled group of plain buttons, the CampaignSwitcher
                // precedent — not role=listbox: a short list with no
                // roving-tabindex/arrow-key model must not announce one. The
                // already-bound member carries aria-current.
                <ul
                  className="gx-playerform__picker"
                  role="group"
                  aria-label={t("components.voiceChannelMembers")}
                >
                  {members.map((m) => (
                    <li key={m.discordUserId}>
                      <button
                        type="button"
                        className="gx-playerform__picker-item"
                        aria-current={m.discordUserId === discordUserId || undefined}
                        onClick={() => pickMember(m)}
                      >
                        <Avatar name={m.displayName} src={m.avatarUrl || null} size="sm" />
                        <span className="gx-playerform__picker-name">{m.displayName}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
        {idInvalid ? (
          <span className="gx-field__hint gx-field__hint--error" role="alert">
            {t("components.discordUserIdDigitsOnly")}
          </span>
        ) : (
          <span className="gx-field__hint">
            {boundMember
              ? t("components.bindsTo", { name: boundMember.displayName })
              : t("components.bindHint")}
          </span>
        )}
      </div>

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
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
