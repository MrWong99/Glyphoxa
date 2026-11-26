// The app-wide register of writes a screen is holding back on purpose — a
// dismissal waiting out its Undo window — that must reach the server before the
// Active Campaign changes.
//
// The Active Campaign is ambient server state (ADR-0039): a request carries no
// campaign id, so a write is resolved against whatever campaign is active when
// it ARRIVES. A write held for Undo and sent only when the screen unmounts would
// arrive after a campaign switch — the screen remounts on the new id only once
// the switch has landed — and land in (or be refused by) the wrong campaign.
// useCampaignSwitch therefore awaits flushPendingWrites() before it writes the
// new selection.

type Flush = () => Promise<unknown>;

const flushes = new Set<Flush>();

/**
 * Lists a flush for as long as a write is held back; returns the function that
 * removes it again (call it once the write is sent or undone).
 */
export function registerPendingWrites(flush: Flush): () => void {
  flushes.add(flush);
  return () => {
    flushes.delete(flush);
  };
}

/** True while any flush is listed, i.e. something is being held back. */
export function hasPendingWrites(): boolean {
  return flushes.size > 0;
}

/**
 * Sends every held-back write now and resolves once each has settled. A failed
 * write does not reject this: the screen that held it reports its own failure,
 * and the caller goes ahead either way.
 */
export async function flushPendingWrites(): Promise<void> {
  await Promise.allSettled([...flushes].map((flush) => flush()));
}
