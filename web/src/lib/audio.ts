// Browser audio playback for the Preview-voice affordance (#70). The VoiceService
// PreviewVoice RPC returns a self-contained WAV blob; this wraps it in an object
// URL and plays it through a transient <audio> element. The URL is revoked when
// playback ends, when the media element errors, or when play() rejects (e.g. an
// autoplay-policy block) — otherwise a failed preview leaks the blob for the
// document lifetime (#154). The play() rejection is surfaced to the caller so a
// blocked preview is distinguishable from silence. It is defensively no-op
// outside a real browser (e.g. jsdom under test, where URL.createObjectURL is
// unimplemented) so callers can fire-and-forget.
export function playAudioBlob(audio: Uint8Array, mimeType: string): Promise<void> {
  let url: string;
  let el: HTMLAudioElement;
  try {
    if (typeof Audio === "undefined" || typeof URL?.createObjectURL !== "function") {
      return Promise.resolve();
    }
    const blob = new Blob([audio as BlobPart], { type: mimeType || "audio/wav" });
    url = URL.createObjectURL(blob);
    el = new Audio(url);
    el.addEventListener("ended", () => URL.revokeObjectURL(url), { once: true });
    el.addEventListener("error", () => URL.revokeObjectURL(url), { once: true });
  } catch {
    return Promise.resolve();
  }
  return Promise.resolve(el.play()).catch((err: unknown) => {
    URL.revokeObjectURL(url);
    throw err;
  });
}
