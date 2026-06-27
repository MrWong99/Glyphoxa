// Browser audio playback for the Preview-voice affordance (#70). The VoiceService
// PreviewVoice RPC returns a self-contained WAV blob; this wraps it in an object
// URL and plays it through a transient <audio> element, revoking the URL when
// playback ends. It is defensively no-op outside a real browser (e.g. jsdom under
// test, where URL.createObjectURL is unimplemented) so callers can fire-and-forget.
export function playAudioBlob(audio: Uint8Array, mimeType: string): HTMLAudioElement | null {
  try {
    if (typeof Audio === "undefined" || typeof URL?.createObjectURL !== "function") {
      return null;
    }
    const blob = new Blob([audio as BlobPart], { type: mimeType || "audio/wav" });
    const url = URL.createObjectURL(blob);
    const el = new Audio(url);
    el.addEventListener("ended", () => URL.revokeObjectURL(url));
    void el.play();
    return el;
  } catch {
    return null;
  }
}
