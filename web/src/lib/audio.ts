// Browser audio playback for the Preview-voice affordance (#70). The VoiceService
// PreviewVoice RPC returns a self-contained WAV blob; this wraps it in an object
// URL and plays it through a transient <audio> element.
//
// Lifecycle hardening (#154): the element is appended to the document (hidden)
// until playback finishes, pinning a strong reference so nothing — GC pressure
// or page tooling that manipulates detached media — can interrupt it with "The
// play() request was interrupted because the media was removed from the
// document". Element and object URL are both released when playback ends, when
// the media element errors, or when play() rejects (e.g. an autoplay-policy
// block) — otherwise a failed preview leaks the blob for the document lifetime.
// The play() rejection is surfaced to the caller so a blocked preview is
// distinguishable from silence. It is defensively no-op outside a real browser
// (e.g. jsdom under test, where URL.createObjectURL is unimplemented) so
// callers can fire-and-forget.
export function playAudioBlob(audio: Uint8Array, mimeType: string): Promise<void> {
  let el: HTMLAudioElement;
  let cleanup: () => void;
  try {
    if (typeof Audio === "undefined" || typeof URL?.createObjectURL !== "function") {
      return Promise.resolve();
    }
    const blob = new Blob([audio as BlobPart], { type: mimeType || "audio/wav" });
    const url = URL.createObjectURL(blob);
    el = new Audio(url);
    let done = false;
    cleanup = () => {
      if (done) return;
      done = true;
      URL.revokeObjectURL(url);
      el.remove();
    };
    el.addEventListener("ended", cleanup, { once: true });
    el.addEventListener("error", cleanup, { once: true });
    el.style.display = "none";
    document.body.appendChild(el);
  } catch {
    return Promise.resolve();
  }
  return Promise.resolve(el.play()).catch((err: unknown) => {
    cleanup();
    throw err;
  });
}
