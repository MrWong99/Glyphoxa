import { useEffect, useId, useRef, useState } from "react";
import { Command } from "cmdk";
import { ChevronDown, Check, Search } from "lucide-react";

// Combobox — a filterable, height-bounded picker for large/growing option lists
// (the live ElevenLabs voice catalog, #88 slice 2). The plain Radix Select can't
// filter, so this pairs cmdk (the ADR-0017 combobox library) with a lightweight
// popover. It reuses the gx-select* trigger vocabulary so it reads as the same
// control, and adds gx-combobox* classes for the filterable popover.
//
// cmdk keys selection/active state on each item's `value`; we set that to the
// UNIQUE option value (labels can collide — ElevenLabs voice names are not
// unique, #154) and pass the human-readable label as `keywords` so typeahead
// still matches what the operator sees. The selected option's label renders on
// the trigger.

type Option = { value: string; label: string };

export function Combobox({
  label = null,
  options,
  value,
  onValueChange,
  disabled = false,
  placeholder = "Select…",
  searchPlaceholder = "Search…",
  emptyText = "No matches",
  id,
  "aria-label": ariaLabel,
}: {
  label?: string | null;
  options: Option[];
  value?: string;
  onValueChange?: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyText?: string;
  id?: string;
  "aria-label"?: string;
}) {
  const generatedId = useId();
  const fid = id || generatedId;
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const wrapRef = useRef<HTMLDivElement>(null);

  const selected = options.find((o) => o.value === value);

  // Close on outside click / Escape so the popover behaves like a native select.
  // Whenever the popover is closed — pick, Escape or outside click — the search
  // resets so the next open shows the full, unfiltered list (#154).
  useEffect(() => {
    if (!open) {
      setSearch("");
      return;
    }
    const onDown = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const pick = (optValue: string) => {
    onValueChange?.(optValue);
    setOpen(false); // the close effect clears the search
  };

  return (
    <div className="gx-select-field gx-combobox" ref={wrapRef}>
      {label && (
        <label className="gx-select-field__label" htmlFor={fid}>
          {label}
        </label>
      )}
      <div className="gx-combobox__anchor">
        <button
          type="button"
          id={fid}
          className="gx-select gx-combobox__trigger"
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={ariaLabel || label || undefined}
          disabled={disabled}
          data-state={open ? "open" : "closed"}
          onClick={() => setOpen((o) => !o)}
        >
          <span className={selected ? "gx-combobox__value" : "gx-combobox__placeholder"}>
            {selected ? selected.label : placeholder}
          </span>
          <ChevronDown size={14} className="gx-select-chevron" />
        </button>

        {open && (
          <div className="gx-select__content gx-combobox__content">
            <Command className="gx-combobox__command" label={ariaLabel || label || "Options"}>
              <div className="gx-combobox__search">
                <Search size={14} className="gx-combobox__search-icon" />
                <Command.Input
                  className="gx-combobox__input"
                  placeholder={searchPlaceholder}
                  value={search}
                  onValueChange={setSearch}
                  autoFocus
                />
              </div>
              <Command.List className="gx-combobox__list">
                <Command.Empty className="gx-combobox__empty">{emptyText}</Command.Empty>
                {options.map((o) => (
                  <Command.Item
                    key={o.value}
                    value={o.value}
                    keywords={[o.label]}
                    className="gx-select__item gx-combobox__item"
                    onSelect={() => pick(o.value)}
                  >
                    <span>{o.label}</span>
                    {o.value === value && <Check size={13} />}
                  </Command.Item>
                ))}
              </Command.List>
            </Command>
          </div>
        )}
      </div>
    </div>
  );
}
