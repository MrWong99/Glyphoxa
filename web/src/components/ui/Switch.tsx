import { useId } from "react";
import * as RSwitch from "@radix-ui/react-switch";

// Switch — ported from the handoff components/forms/Switch.jsx onto a Radix
// Switch (ADR-0017). The Radix Root carries .gx-switch__track and emits
// [data-state=checked]; the Thumb carries .gx-switch__thumb. Lights arcane
// violet when on.

export function Switch({
  checked,
  defaultChecked,
  onCheckedChange,
  label = null,
  disabled = false,
  id,
}: {
  checked?: boolean;
  defaultChecked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
  label?: string | null;
  disabled?: boolean;
  id?: string;
}) {
  const generatedId = useId();
  const fid = id || generatedId;
  return (
    <span className={"gx-switch" + (disabled ? " gx-switch--disabled" : "")}>
      <RSwitch.Root
        id={fid}
        className="gx-switch__track"
        checked={checked}
        defaultChecked={defaultChecked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
      >
        <RSwitch.Thumb className="gx-switch__thumb" />
      </RSwitch.Root>
      {label && (
        <label className="gx-switch__label" htmlFor={fid}>
          {label}
        </label>
      )}
    </span>
  );
}
