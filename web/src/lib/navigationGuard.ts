// The app-wide register of "something on screen has unsaved edits". Screens
// never talk to the router (they stay router-free, and router-mock-free in
// tests): an editor reports its dirtiness through useDirtyGuard, which lists a
// probe here, and the ROUTE layer (NavigationGuard in app/RouteFallbacks.tsx)
// asks hasUnsavedEdits() before letting a navigation or a tab close through.
// That is what makes a sidebar click — which unmounts an editor the screen
// itself never sees coming — guardable at all.

type Probe = () => boolean;

const probes = new Set<Probe>();

/** Lists a dirtiness probe; returns the function that removes it again. */
export function registerUnsavedEditsProbe(probe: Probe): () => void {
  probes.add(probe);
  return () => {
    probes.delete(probe);
  };
}

/** True while any mounted editor reports unsaved edits. */
export function hasUnsavedEdits(): boolean {
  for (const probe of probes) {
    if (probe()) return true;
  }
  return false;
}

// Guarded selection keys (ADR-0063 amendment). Most query changes on a screen
// are harmless — the deep-link "consume then strip" rewrites unmount nothing —
// so the route guard only ever looked at the PATH. A screen that keeps its
// selection in the URL (?node=, ?map=, ?thread= …) changes that: there, a
// query change REPLACES what the open editor is editing (browser Back to the
// previous entry, a palette jump to another one). Such a screen declares the
// keys that carry its selection while it is mounted; NavigationGuard then also
// holds a navigation that changes one of them while edits are unsaved.
//
// Counted, not a plain set: two mounted components may declare the same key,
// and the first to unmount must not lift the other's declaration.
const guardedKeys = new Map<string, number>();

/** Declares search keys as guarded selection state; returns the undo. */
export function registerGuardedSearchKeys(keys: readonly string[]): () => void {
  for (const key of keys) guardedKeys.set(key, (guardedKeys.get(key) ?? 0) + 1);
  let released = false;
  return () => {
    // Idempotent: a double release (StrictMode's effect replay) must not eat
    // another component's count.
    if (released) return;
    released = true;
    for (const key of keys) {
      const left = (guardedKeys.get(key) ?? 0) - 1;
      if (left > 0) guardedKeys.set(key, left);
      else guardedKeys.delete(key);
    }
  };
}

/**
 * True when a navigation from `current` to `next` search changes a key some
 * mounted screen declared as guarded selection state.
 */
export function guardedSearchChanged(
  current: Record<string, unknown> | undefined,
  next: Record<string, unknown> | undefined,
): boolean {
  for (const key of guardedKeys.keys()) {
    if (current?.[key] !== next?.[key]) return true;
  }
  return false;
}
