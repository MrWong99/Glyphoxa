import "@testing-library/jest-dom/vitest";
import { beforeEach } from "vitest";

import { MockEventSource } from "./src/test/mockEventSource";

// jsdom does not implement HTMLMediaElement.play(); the Preview-voice helper
// (src/lib/audio.ts) fires it fire-and-forget. Stub it so the noise stays out of
// the test output — the observable behaviour under test is the PreviewVoice RPC
// call, not real playback.
if (typeof HTMLMediaElement !== "undefined") {
  HTMLMediaElement.prototype.play = () => Promise.resolve();
  HTMLMediaElement.prototype.pause = () => {};
}

// jsdom implements neither ResizeObserver nor Element.scrollIntoView; cmdk (the
// Voice Combobox, #88 slice 2) uses both. Stub them so the filterable popover
// mounts and highlights items under test — the observable behaviour is the
// filtering/selection, not layout measurement.
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}
if (typeof Element !== "undefined" && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// jsdom has no EventSource; the Session screen opens one for the live transcript
// (#73). Install the mock globally so every test renders without crashing, and a
// transcript test can drive frames via MockEventSource.last().
globalThis.EventSource = MockEventSource as unknown as typeof EventSource;

// Default snapshot fetch: a benign empty-but-live transcript so a session-active
// render never hits the network. Transcript tests override globalThis.fetch.
beforeEach(() => {
  MockEventSource.reset();
  globalThis.fetch = (async () =>
    new Response(JSON.stringify({ lines: [], status: "live", typing: { active: false, label: "" } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })) as typeof fetch;
});
