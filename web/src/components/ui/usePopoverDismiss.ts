import { useEffect, useRef, type RefObject } from "react";

// usePopoverDismiss closes a lightweight (non-Radix) popover the way a native
// select would: a mousedown outside the anchor element or an Escape keypress
// calls onClose. Extracted from the Combobox popover (#88 slice 2) when the
// CampaignSwitcher grew an identical copy (#267), so dismissal fixes (touch /
// pointer events, stacked popovers, focus handling) land in one place.
export function usePopoverDismiss(
  ref: RefObject<HTMLElement | null>,
  open: boolean,
  onClose: () => void,
) {
  // The latest onClose rides a ref so the document listeners — attached once per
  // open — never call a stale closure, and re-renders don't churn listeners.
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onCloseRef.current();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCloseRef.current();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, ref]);
}
