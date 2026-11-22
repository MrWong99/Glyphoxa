import { useId, type ReactNode } from "react";

import { useI18n } from "@/i18n";

// Field — the label / hint / error frame around ANY control, with the ARIA
// wiring Input has built in: the label names the control, the hint or error is
// announced with it (aria-describedby), and an error marks it invalid. Use it
// for controls that are not a plain Input: a Select, a Combobox, a ChipInput,
// a SegmentedControl, a custom picker.
//
// The child is a function that receives the props to spread on the control:
//
//   <Field label="Voice" hint="Used for every line this NPC speaks." error={voiceError}>
//     {(control) => <VoicePicker {...control} value={voice} onChange={setVoice} />}
//   </Field>
//
// For a control made of several elements (a group of buttons, a chip list)
// pass group: the frame becomes a role=group named by the label, and the
// control props carry no id.
//
// An error replaces the hint (one line under the control, like Input) and is
// what a screen reader hears as the control's description.

export type FieldControlProps = {
  /** Absent for a group field. */
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
  "aria-required"?: true;
};

export function Field({
  label,
  hint,
  error,
  required = false,
  group = false,
  id,
  className = "",
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  /** Marks the label and sets aria-required on the control. */
  required?: boolean;
  /** The control is several elements: label the frame as a group instead of one element. */
  group?: boolean;
  /** Id for the control; generated when omitted. */
  id?: string;
  className?: string;
  children: (control: FieldControlProps) => ReactNode;
}) {
  const { t } = useI18n();
  const generatedId = useId();
  const fid = id || generatedId;
  const labelId = `${fid}-label`;
  const hintId = `${fid}-hint`;
  const invalid = Boolean(error);
  const described = error || hint ? hintId : undefined;

  const control: FieldControlProps = {
    ...(group ? {} : { id: fid }),
    ...(described ? { "aria-describedby": described } : {}),
    ...(invalid ? { "aria-invalid": true as const } : {}),
    ...(required ? { "aria-required": true as const } : {}),
  };

  const requiredMark = required && (
    <>
      <span className="gx-field__required" aria-hidden="true">
        *
      </span>{" "}
      <span className="gx-visually-hidden">({t("common.required")})</span>
    </>
  );

  const cls = ["gx-field", group ? "gx-field--group" : "", className].filter(Boolean).join(" ");

  return (
    <div
      className={cls}
      {...(group ? { role: "group", "aria-labelledby": labelId, "aria-describedby": described } : {})}
    >
      {group ? (
        <span className="gx-field__label" id={labelId}>
          {label}
          {requiredMark}
        </span>
      ) : (
        <label className="gx-field__label" id={labelId} htmlFor={fid}>
          {label}
          {requiredMark}
        </label>
      )}
      {children(control)}
      {(error || hint) && (
        <span id={hintId} className={"gx-field__hint" + (error ? " gx-field__hint--error" : "")}>
          {error || hint}
        </span>
      )}
    </div>
  );
}
