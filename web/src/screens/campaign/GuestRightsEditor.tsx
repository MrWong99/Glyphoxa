import { useId } from "react";
import { Lock } from "lucide-react";

import type { GuestRight } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import {
  GUEST_RIGHTS,
  GUEST_RIGHT_GROUPS,
  GUEST_RIGHT_PRESETS,
  lockedGuestRights,
  matchingPreset,
  normalizeGuestRights,
  toggleGuestRight,
} from "@/lib/guestRights";

// The Guest Rights editor (ADR-0065): what one Campaign Guest — or everyone who
// opens one invitation link — may do in the Campaign. Grouped checkboxes, each
// with one plain sentence on what ticking it reveals or costs, under three
// presets that are starting points, not modes: a preset sets the boxes and the
// GM changes any of them afterwards.
//
// Three rights imply others (lib/guestRights.ts mirrors the server's
// normalization). An implied box is ticked AND locked, with a line naming the
// right that needs it — so the GM sees before saving that "edit the wiki" also
// shows the GM-only entries, instead of finding the set changed after it.
//
// Controlled, and always handed the normalized set: `value` is what would be
// stored, so a caller can send it as is.

export function GuestRightsEditor({
  value,
  onChange,
  disabled = false,
}: {
  value: readonly GuestRight[];
  onChange: (rights: GuestRight[]) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const baseId = useId();
  const rights = normalizeGuestRights(value);
  const locked = lockedGuestRights(rights);
  const preset = matchingPreset(rights);
  const fullLabel = (right: GuestRight) => {
    const info = GUEST_RIGHTS.find((r) => r.right === right);
    return info ? t(info.label) : "";
  };

  return (
    <div className="gx-rights">
      <div className="gx-rights__presets" role="group" aria-label={t("access.presetsLabel")}>
        {GUEST_RIGHT_PRESETS.map((p) => (
          <button
            key={p.id}
            type="button"
            className="gx-rights__preset"
            aria-pressed={preset?.id === p.id}
            disabled={disabled}
            onClick={() => onChange(normalizeGuestRights(p.rights))}
          >
            <span className="gx-rights__preset-name">{t(p.label)}</span>
            <span className="gx-rights__preset-desc">{t(p.description)}</span>
          </button>
        ))}
      </div>

      {GUEST_RIGHT_GROUPS.map((group) => (
        <fieldset key={group.id} className="gx-rights__group">
          <legend className="gx-rights__legend">{t(group.label)}</legend>
          {group.choices.map((choice) => {
            const id = `${baseId}-${choice.right}`;
            const holders = locked.get(choice.right);
            return (
              <div key={choice.right} className="gx-rights__row" data-locked={holders ? "true" : undefined}>
                <input
                  id={id}
                  type="checkbox"
                  checked={rights.includes(choice.right)}
                  // A locked box cannot be cleared here: the right that needs
                  // it has to be cleared first, and the line below says which.
                  disabled={disabled || holders !== undefined}
                  aria-describedby={`${id}-desc`}
                  onChange={(e) => onChange(toggleGuestRight(rights, choice.right, e.target.checked))}
                />
                <div className="gx-rights__text">
                  <label htmlFor={id} className="gx-rights__label">
                    {t(choice.label)}
                  </label>
                  <span id={`${id}-desc`} className="gx-rights__desc">
                    {t(choice.description)}
                    {holders && (
                      <span className="gx-rights__locked">
                        <Lock size={11} aria-hidden="true" />{" "}
                        {t("access.rightIncludedWith", { right: holders.map(fullLabel).join(", ") })}
                      </span>
                    )}
                  </span>
                </div>
              </div>
            );
          })}
        </fieldset>
      ))}
    </div>
  );
}
