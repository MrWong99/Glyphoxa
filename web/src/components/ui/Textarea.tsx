import { forwardRef, useCallback, useEffect, useId, useLayoutEffect, useRef } from "react";
import type { ReactNode, TextareaHTMLAttributes } from "react";

// Textarea — the long-form sibling of Input, with the same contract: `label`
// names it, `hint` or `error` sits under it and is announced with it
// (aria-describedby), an error marks it invalid. Same .gx-field / .gx-input
// classes, so a form mixing the two lines up.
//
// autoGrow makes the box as tall as its text: it starts at `rows` and grows
// line by line, up to `maxRows` (then it scrolls). For a chat composer, a
// description, a persona: anything where a fixed box either wastes space or
// hides what was typed.
//
// forwardRef, so a caller can focus it or read its selection.

type TextareaProps = {
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  /** Grow with the content instead of showing a resize handle. */
  autoGrow?: boolean;
  /** With autoGrow: the height stops growing here and the box scrolls. */
  maxRows?: number;
} & TextareaHTMLAttributes<HTMLTextAreaElement>;

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { label = null, hint = null, error = null, autoGrow = false, maxRows, id, className = "", rows, onInput, ...props },
  forwardedRef,
) {
  const generatedId = useId();
  const fid = id || generatedId;
  const invalid = Boolean(error);
  const describedBy = error || hint ? `${fid}-hint` : undefined;
  const cls = ["gx-input", autoGrow ? "gx-input--autogrow" : "", invalid ? "gx-input--invalid" : "", className]
    .filter(Boolean)
    .join(" ");

  const localRef = useRef<HTMLTextAreaElement | null>(null);
  const setRefs = useCallback(
    (node: HTMLTextAreaElement | null) => {
      localRef.current = node;
      if (typeof forwardedRef === "function") forwardedRef(node);
      else if (forwardedRef) forwardedRef.current = node;
    },
    [forwardedRef],
  );

  const fit = useCallback(() => {
    const el = localRef.current;
    if (!el || !autoGrow) return;
    // Measuring collapses the box for an instant, which shortens the page. A
    // scroll container that is scrolled near its end (the content area, a
    // dialog body, the window) then has its scrollTop clamped by the layout
    // the measurement forces, and nothing restores it: the view jumped to the
    // top on every keystroke in a long text. Note where each scrolled ancestor
    // is and put it back once the real height is set. All of it happens in
    // this one synchronous pass, so nothing is painted in between. (Only a
    // real browser shows the jump; jsdom has no layout.)
    const scrolled: Array<[Element, number]> = [];
    for (let node = el.parentElement; node; node = node.parentElement) {
      if (node.scrollTop !== 0) scrolled.push([node, node.scrollTop]);
    }
    // Collapse first, so the box can also shrink when text is deleted.
    el.style.height = "auto";
    // Not laid out (hidden tab, test DOM): leave the height to `rows`.
    if (el.scrollHeight === 0) return;
    const style = window.getComputedStyle(el);
    const borders = (parseFloat(style.borderTopWidth) || 0) + (parseFloat(style.borderBottomWidth) || 0);
    let height = el.scrollHeight + borders;
    let capped = false;
    if (maxRows) {
      const lineHeight = parseFloat(style.lineHeight) || 1.5 * (parseFloat(style.fontSize) || 14);
      const padding = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
      const cap = lineHeight * maxRows + padding + borders;
      if (height > cap) {
        height = cap;
        capped = true;
      }
    }
    el.style.height = `${height}px`;
    for (const [node, top] of scrolled) {
      if (node.scrollTop !== top) node.scrollTop = top;
    }
    // The stylesheet shows the scrollbar only once the cap is reached.
    if (capped) el.dataset.capped = "true";
    else delete el.dataset.capped;
  }, [autoGrow, maxRows]);

  // A controlled value (or the first paint) changes the text without an input
  // event; refit before the browser paints so the box never flickers.
  useLayoutEffect(fit, [fit, props.value]);

  // The same text needs a different height when the box gets narrower.
  useEffect(() => {
    const el = localRef.current;
    if (!el || !autoGrow || typeof ResizeObserver === "undefined") return;
    let width = el.clientWidth;
    const observer = new ResizeObserver(() => {
      if (el.clientWidth !== width) {
        width = el.clientWidth;
        fit();
      }
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [autoGrow, fit]);

  return (
    <div className="gx-field">
      {label && (
        <label className="gx-field__label" htmlFor={fid}>
          {label}
        </label>
      )}
      <textarea
        ref={setRefs}
        id={fid}
        className={cls}
        rows={rows ?? (autoGrow ? 1 : undefined)}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        onInput={(event) => {
          fit();
          onInput?.(event);
        }}
        {...props}
      />
      {(error || hint) && (
        <span id={describedBy} className={"gx-field__hint" + (error ? " gx-field__hint--error" : "")}>
          {error || hint}
        </span>
      )}
    </div>
  );
});
