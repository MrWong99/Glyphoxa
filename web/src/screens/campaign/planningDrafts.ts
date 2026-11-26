// Composer drafts for the Planning chat, one per campaign and conversation
// (plus one for a conversation not yet started), in sessionStorage: a draft
// survives a visit to another view, another conversation or a reload of the
// tab, and is gone with the tab. Storage can be unavailable (private mode,
// blocked site data); then drafts simply last as long as the panel does.

const PREFIX = "gx-planning-draft";

/** The draft slot for a conversation; null is the one not yet started. */
export function draftKey(campaignId: string, threadId: string | null): string {
  return `${PREFIX}:${campaignId}:${threadId ?? "new"}`;
}

export function loadDraft(campaignId: string, threadId: string | null): string {
  try {
    return window.sessionStorage.getItem(draftKey(campaignId, threadId)) ?? "";
  } catch {
    return "";
  }
}

export function saveDraft(campaignId: string, threadId: string | null, text: string): void {
  try {
    const key = draftKey(campaignId, threadId);
    if (text === "") window.sessionStorage.removeItem(key);
    else window.sessionStorage.setItem(key, text);
  } catch {
    // Unavailable storage: the draft lives in the panel's state only.
  }
}

/**
 * MESSAGE_MAX mirrors planchat.MaxMessageRunes: the server counts code points
 * (Go runes) of the trimmed message, so the composer counts the same way — a
 * UTF-16 length would let an emoji-heavy message pass here and fail there.
 */
export const MESSAGE_MAX = 4000;

/** The composer shows its counter from here on. */
export const MESSAGE_COUNTER_FROM = 3500;

/** Code points of the trimmed message, the server's own measure. */
export function messageLength(text: string): number {
  return Array.from(text.trim()).length;
}
