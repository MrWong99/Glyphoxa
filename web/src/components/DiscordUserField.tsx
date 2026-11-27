import { useId, useMemo, useRef, useState } from "react";
import { Users } from "lucide-react";

import type { DiscordVoiceMember } from "@gen/glyphoxa/management/v1/management_pb";
import { Avatar } from "@/components/ui/Avatar";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { useI18n } from "@/i18n";

import "./playerBindForm.css";

// DiscordUserField names one Discord User: a live voice-channel member picker
// (the Bot's current voice states, #279) paired with a free-text snowflake
// field. It was the Character bind form's last field and is now shared with the
// Campaign's Sharing view (ADR-0065: "add someone" by picking them from the
// voice channel or typing their ID), so both name a person the same way.
//
// It holds no RPC and no validation of its own: the caller passes the members
// it read (an empty list when the Bot is offline — only the free-text path
// shows then), says what is wrong with the typed value, if anything, and says
// which members cannot be picked and why.

export function DiscordUserField({
  value,
  onChange,
  members,
  error = null,
  unavailable,
  memberHint,
  onEnter,
}: {
  value: string;
  onChange: (discordUserId: string) => void;
  members: readonly DiscordVoiceMember[];
  /** What is wrong with the typed id, already in words; null when nothing is. */
  error?: string | null;
  /** Why a member cannot be picked (already linked, already a guest), or undefined when they can. */
  unavailable?: (discordUserId: string) => string | undefined;
  /** The hint once the id is a member the picker knows. Default: "Links to {name}." */
  memberHint?: (name: string) => string;
  onEnter?: () => void;
}) {
  const { t } = useI18n();
  const [pickerOpen, setPickerOpen] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);
  const hintId = useId();
  usePopoverDismiss(pickerRef, pickerOpen, () => setPickerOpen(false));

  // The member a known id resolves to, so the hint can say who the digits are.
  const member = useMemo(() => members.find((m) => m.discordUserId === value), [members, value]);

  return (
    <div className="gx-field" ref={pickerRef}>
      <span className="gx-field__label">{t("components.discordUserLabel")}</span>
      <div className="gx-playerform__bind">
        <Input
          aria-label={t("components.discordUserIdLabel")}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t("components.discordUserIdPlaceholder")}
          aria-invalid={error !== null || undefined}
          aria-describedby={hintId}
          inputMode="numeric"
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onEnter?.();
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
              <ul className="gx-playerform__picker" role="group" aria-label={t("components.voiceChannelMembers")}>
                {members.map((m) => {
                  // A member who cannot be picked stays in the list, named with
                  // the reason: absent, the GM would look for them in vain.
                  const reason = unavailable?.(m.discordUserId);
                  return (
                    <li key={m.discordUserId}>
                      <button
                        type="button"
                        className="gx-playerform__picker-item"
                        aria-current={m.discordUserId === value || undefined}
                        disabled={reason !== undefined}
                        onClick={() => {
                          onChange(m.discordUserId);
                          setPickerOpen(false);
                        }}
                      >
                        <Avatar name={m.displayName} src={m.avatarUrl || null} size="sm" />
                        <span className="gx-playerform__picker-name">{m.displayName}</span>
                        {reason !== undefined && <span className="gx-playerform__picker-taken">{reason}</span>}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )}
      </div>
      {error !== null ? (
        <span id={hintId} className="gx-field__hint gx-field__hint--error" role="alert">
          {error}
        </span>
      ) : (
        <span id={hintId} className="gx-field__hint">
          {member
            ? (memberHint?.(member.displayName) ?? t("components.bindsTo", { name: member.displayName }))
            : members.length > 0
              ? t("components.bindHint")
              : t("players.bindHintNoVoice")}
        </span>
      )}
      {/* A Discord user id is not something a GM has at hand, and nothing in
          Discord's own UI shows it until Developer Mode is on. */}
      <details className="gx-playerform__help">
        <summary>{t("players.idHelpSummary")}</summary>
        <ol>
          <li>{t("players.idHelpStep1")}</li>
          <li>{t("players.idHelpStep2")}</li>
          <li>{t("players.idHelpStep3")}</li>
        </ol>
      </details>
    </div>
  );
}
