// The app-wide register of "something on screen has unsaved edits". Screens
// never talk to the router (they stay router-free, and router-mock-free in
// tests): an editor reports its dirtiness through useDirtyGuard, which lists a
// probe here, and the ROUTE layer (NavigationGuard in app/router.tsx) asks
// hasUnsavedEdits() before letting a navigation or a tab close through. That
// is what makes a sidebar click — which unmounts an editor the screen itself
// never sees coming — guardable at all.

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
