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
//
// Two optional hooks for a caller that offers a Stop button (the voice picker):
// `signal` stops the playback and releases the element when it aborts, and
// `onEnded` is called once when the playback is over on its own account
// (finished, errored mid-playback, or never started because there is no media
// pipeline). It is not called for an abort or a rejected play(): the caller did
// the one and hears about the other through the returned promise.
//
// A clip the browser cannot load (unsupported or corrupt media) fires the
// element's "error" event FIRST and only then rejects the pending play() with
// NotSupportedError (the media failure steps; Chromium orders them the same
// way). An "error" before play() has settled therefore only releases the
// element: reporting it as "ended" would let the caller go idle and drop the
// rejection that carries the reason.
export function playAudioBlob(
  audio: Uint8Array,
  mimeType: string,
  opts: { signal?: AbortSignal; onEnded?: () => void } = {},
): Promise<void> {
  const { signal, onEnded } = opts;
  if (signal?.aborted) return Promise.resolve();
  let el: HTMLAudioElement;
  let cleanup: () => void;
  // play() has resolved; an "error" before that is reported by its rejection.
  let started = false;
  let failedEarly = false;
  try {
    if (typeof Audio === "undefined" || typeof URL?.createObjectURL !== "function") {
      onEnded?.();
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
    const over = () => {
      if (done) return;
      cleanup();
      onEnded?.();
    };
    el.addEventListener("ended", over, { once: true });
    el.addEventListener(
      "error",
      () => {
        if (started) over();
        else {
          failedEarly = true;
          cleanup();
        }
      },
      { once: true },
    );
    signal?.addEventListener(
      "abort",
      () => {
        try {
          el.pause();
        } catch {
          // No media pipeline to pause; releasing the element is what matters.
        }
        cleanup();
      },
      { once: true },
    );
    el.style.display = "none";
    document.body.appendChild(el);
  } catch {
    onEnded?.();
    return Promise.resolve();
  }
  return Promise.resolve(el.play()).then(
    () => {
      started = true;
      // An "error" came first, yet play() resolved: nothing is playing, so the
      // playback is over all the same.
      if (failedEarly) onEnded?.();
    },
    (err: unknown) => {
      cleanup();
      throw err;
    },
  );
}
