import { useId, useRef, useState } from "react";
import type { ClipboardEvent, KeyboardEvent, ReactNode } from "react";
import { X } from "lucide-react";

import { useI18n } from "@/i18n";

// ChipInput — edit a short list of strings (an NPC's aliases, an entry's tags)
// in one box: each value is a chip with a remove button, and a text field at
// the end adds the next one. Ported from the alias control in PlayerBindForm
// so the Cast, Players and wiki editors stop each growing their own.
//
// Keyboard, all from the text field:
//   Enter or comma   add what is typed (pasting "a, b, c" adds three)
//   Backspace        with nothing typed, remove the last chip
//   Tab / blur       add what is typed, so text never sits there unsaved when
//                    the GM clicks Save (the old control lost it silently)
// Each chip's remove button is a normal tab stop named "Remove <value>".
//
// It is controlled: `values` in, `onChange` with the next list out. Values are
// trimmed, an empty or already-present value is dropped, and `max` caps the
// list. Same label / hint / error contract as Input.

type ChipInputProps = {
  values: readonly string[];
  onChange: (values: string[]) => void;
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  placeholder?: string;
  /** Upper bound on the number of values; the text field is disabled at the cap. */
  max?: number;
  disabled?: boolean;
  /** Add the typed text when the field loses focus. Default true. */
  commitOnBlur?: boolean;
  /** Reports the uncommitted text, for a form that counts it as an unsaved edit. */
  onDraftChange?: (draft: string) => void;
  id?: string;
  /** Names the text field when there is no visible label. */
  "aria-label"?: string;
  /** Extra description ids, e.g. from a surrounding Field. */
  "aria-describedby"?: string;
};

const SEPARATOR = /[,\n]/;

export function ChipInput({
  values,
  onChange,
  label = null,
  hint = null,
  error = null,
  placeholder,
  max,
  disabled = false,
  commitOnBlur = true,
  onDraftChange,
  id,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
}: ChipInputProps) {
  const { t } = useI18n();
  const generatedId = useId();
  const fid = id || generatedId;
  const inputRef = useRef<HTMLInputElement>(null);
  const [draft, setDraftState] = useState("");

  const invalid = Boolean(error);
  const hintId = error || hint ? `${fid}-hint` : undefined;
  const describedBy = [ariaDescribedBy, hintId].filter(Boolean).join(" ") || undefined;
  const full = max !== undefined && values.length >= max;

  const setDraft = (next: string) => {
    setDraftState(next);
    onDraftChange?.(next);
  };

  // add commits one or more candidate values and clears the draft. It reports
  // a change only when the list really grows.
  const add = (candidates: string[]) => {
    const next = [...values];
    for (const candidate of candidates) {
      const value = candidate.trim();
      if (value === "" || next.includes(value)) continue;
      if (max !== undefined && next.length >= max) break;
      next.push(value);
    }
    setDraft("");
    if (next.length !== values.length) onChange(next);
  };

  const remove = (value: string) => {
    onChange(values.filter((v) => v !== value));
    // The button that had focus is about to unmount; keep the keyboard user in
    // the control instead of dropping them on <body>.
    inputRef.current?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // An IME uses Enter to confirm a composition; that is not "add".
    if (event.nativeEvent.isComposing) return;
    if (event.key === "Enter") {
      // With nothing typed, Enter is left to the form around this control.
      if (draft.trim() === "") return;
      event.preventDefault();
      add([draft]);
    } else if (event.key === ",") {
      event.preventDefault();
      add([draft]);
    } else if (event.key === "Backspace" && draft === "" && values.length > 0) {
      event.preventDefault();
      onChange(values.slice(0, -1));
    }
  };

  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData("text");
    if (!SEPARATOR.test(text)) return; // a single value pastes as plain text
    event.preventDefault();
    add((draft + text).split(SEPARATOR));
  };

  return (
    <div className="gx-field">
      {label && (
        <label className="gx-field__label" htmlFor={fid}>
          {label}
        </label>
      )}
      <div
        className="gx-chipinput"
        data-invalid={invalid || undefined}
        data-disabled={disabled || undefined}
        // A press on the frame's own padding (not on a chip or the field)
        // lands in the text field, the way it would in a real input.
        onMouseDown={(event) => {
          if (event.target === event.currentTarget) {
            event.preventDefault();
            inputRef.current?.focus();
          }
        }}
      >
        {values.length > 0 && (
          // role=list is stated because the list is display: contents, which
          // some browsers take as a reason to drop the list semantics.
          <ul className="gx-chipinput__list" role="list">
            {values.map((value) => (
              <li key={value} className="gx-chip">
                <span className="gx-chip__label">{value}</span>
                <button
                  type="button"
                  className="gx-chip__remove"
                  aria-label={t("common.removeItem", { item: value })}
                  disabled={disabled}
                  onClick={() => remove(value)}
                >
                  <X size={12} aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <input
          ref={inputRef}
          id={fid}
          className="gx-chipinput__input"
          type="text"
          value={draft}
          placeholder={full ? undefined : placeholder}
          disabled={disabled || full}
          autoComplete="off"
          aria-label={ariaLabel}
          aria-invalid={invalid || undefined}
          aria-describedby={describedBy}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          onBlur={() => {
            if (commitOnBlur && draft.trim() !== "") add([draft]);
          }}
        />
      </div>
      {(error || hint) && (
        <span id={hintId} className={"gx-field__hint" + (error ? " gx-field__hint--error" : "")}>
          {error || hint}
        </span>
      )}
    </div>
  );
}
