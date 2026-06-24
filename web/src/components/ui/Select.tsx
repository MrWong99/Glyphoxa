import { useId } from "react";
import * as RSelect from "@radix-ui/react-select";
import { ChevronDown, Check } from "lucide-react";

// Select — ported from the handoff components/forms/Select.jsx onto a Radix
// Select (ADR-0017: Radix backs the accessibility-heavy primitives). The trigger
// keeps .gx-select + the field wrapper/label keep the handoff class names; the
// portal content/items are styled via .gx-select__content / __item.

type Option = string | { value: string; label: string };

function norm(o: Option): { value: string; label: string } {
  return typeof o === "string" ? { value: o, label: o } : o;
}

export function Select({
  label = null,
  options,
  value,
  defaultValue,
  onValueChange,
  disabled = false,
  placeholder = "Select…",
  id,
  "aria-label": ariaLabel,
}: {
  label?: string | null;
  options: Option[];
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
  id?: string;
  "aria-label"?: string;
}) {
  const generatedId = useId();
  const fid = id || generatedId;
  const opts = options.map(norm);

  return (
    <div className="gx-select-field">
      {label && (
        <label className="gx-select-field__label" htmlFor={fid}>
          {label}
        </label>
      )}
      <RSelect.Root
        value={value}
        defaultValue={defaultValue}
        onValueChange={onValueChange}
        disabled={disabled}
      >
        <RSelect.Trigger className="gx-select" id={fid} aria-label={ariaLabel || label || undefined}>
          <RSelect.Value placeholder={placeholder} />
          <RSelect.Icon className="gx-select-chevron" asChild>
            <ChevronDown size={14} />
          </RSelect.Icon>
        </RSelect.Trigger>
        <RSelect.Portal>
          <RSelect.Content className="gx-select__content" position="popper" sideOffset={4}>
            <RSelect.Viewport>
              {opts.map((o) => (
                <RSelect.Item key={o.value} value={o.value} className="gx-select__item">
                  <RSelect.ItemText>{o.label}</RSelect.ItemText>
                  <RSelect.ItemIndicator>
                    <Check size={13} />
                  </RSelect.ItemIndicator>
                </RSelect.Item>
              ))}
            </RSelect.Viewport>
          </RSelect.Content>
        </RSelect.Portal>
      </RSelect.Root>
    </div>
  );
}
