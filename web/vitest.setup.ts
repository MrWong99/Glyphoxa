import "@testing-library/jest-dom/vitest";

// jsdom does not implement HTMLMediaElement.play(); the Preview-voice helper
// (src/lib/audio.ts) fires it fire-and-forget. Stub it so the noise stays out of
// the test output — the observable behaviour under test is the PreviewVoice RPC
// call, not real playback.
if (typeof HTMLMediaElement !== "undefined") {
  HTMLMediaElement.prototype.play = () => Promise.resolve();
  HTMLMediaElement.prototype.pause = () => {};
}
