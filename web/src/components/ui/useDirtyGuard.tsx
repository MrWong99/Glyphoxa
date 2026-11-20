import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { useI18n } from "@/i18n";
import { registerUnsavedEditsProbe } from "@/lib/navigationGuard";
import { ConfirmDialog } from "./ConfirmDialog";

// useDirtyGuard is the one unsaved-edits primitive (W10). An editor that holds
// typed-but-unsaved state reports it with markDirty; every in-screen action
// that would unmount or re-seed that editor is wrapped in guard(), which runs
// it at once when clean and asks first when dirty. The same flag is listed in
// the app-wide register (lib/navigationGuard), so the route layer can hold a
// sidebar click, a palette jump or a tab close the screen never sees.
//
//   const edits = useDirtyGuard();
//   <Editor onDirtyChange={edits.markDirty} />
//   <button onClick={() => edits.guard(() => select(other))} />
//   {edits.dialog}
//
// Before this, three editors each either hand-rolled a guard for ONE path
// (the roster click) or had none, and silently threw typing away on the rest.
export function useDirtyGuard(
  copy: { title?: ReactNode; description?: ReactNode; confirmLabel?: string } = {},
): {
  /** Report whether the guarded editor currently differs from what is saved. */
  markDirty: (dirty: boolean) => void;
  /** Run `action` now if clean; if dirty, ask, and run it only on confirm. */
  guard: (action: () => void) => void;
  /** Render this once — it is the "Discard unsaved changes?" dialog. */
  dialog: ReactNode;
} {
  const { t } = useI18n();
  // A ref, not state: dirtiness changes on every keystroke and nothing renders
  // off it — it is only read at the moment an action asks.
  const dirty = useRef(false);
  const [pending, setPending] = useState<(() => void) | null>(null);

  useEffect(() => registerUnsavedEditsProbe(() => dirty.current), []);

  const markDirty = useCallback((d: boolean) => {
    dirty.current = d;
  }, []);

  const guard = useCallback((action: () => void) => {
    if (dirty.current) setPending(() => action);
    else action();
  }, []);

  const dialog = pending ? (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) setPending(null);
      }}
      title={copy.title ?? t("common.discardChangesTitle")}
      description={copy.description ?? t("common.discardChangesDesc")}
      confirmLabel={copy.confirmLabel ?? t("common.discardChangesConfirm")}
      onConfirm={() => {
        // The GM chose to lose the edits: lift the guard BEFORE running the
        // action, so an action that itself navigates is not asked twice.
        dirty.current = false;
        setPending(null);
        pending();
      }}
    />
  ) : null;

  return { markDirty, guard, dialog };
}
