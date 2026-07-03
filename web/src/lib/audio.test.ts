import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { playAudioBlob } from "./audio";

// jsdom has no real media pipeline: Audio.play is unimplemented and
// URL.createObjectURL doesn't exist. The stub below stands in a REAL <audio>
// DOM node (so attachment is observable) with a controllable play().
//
// Lifecycle contract under test (#154): Chrome interrupts detached media
// elements ("The play() request was interrupted because the media was removed
// from the document"), so the element must live in the document for the whole
// playback, and both the element and its object URL must be released on
// ended / error / play() rejection.
const instances: HTMLAudioElement[] = [];
let playImpl: () => Promise<void>;

function FakeAudio(src: string): HTMLAudioElement {
  const el = document.createElement("audio");
  el.src = src;
  Object.defineProperty(el, "play", { value: () => playImpl(), configurable: true });
  instances.push(el);
  return el;
}

const createObjectURL = vi.fn(() => "blob:preview");
const revokeObjectURL = vi.fn();

beforeEach(() => {
  instances.length = 0;
  playImpl = () => Promise.resolve();
  createObjectURL.mockClear();
  revokeObjectURL.mockClear();
  vi.stubGlobal("Audio", FakeAudio);
  (URL as unknown as Record<string, unknown>).createObjectURL = createObjectURL;
  (URL as unknown as Record<string, unknown>).revokeObjectURL = revokeObjectURL;
});

afterEach(() => {
  vi.unstubAllGlobals();
  delete (URL as unknown as Record<string, unknown>).createObjectURL;
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
  document.body.innerHTML = "";
});

describe("playAudioBlob", () => {
  it("keeps the element attached (hidden) to the document while playing", async () => {
    await playAudioBlob(new Uint8Array([1, 2]), "audio/wav");

    // Detached media gets interrupted by Chrome — the element must be in the
    // document for the playback duration, invisible to the operator.
    expect(instances[0].isConnected).toBe(true);
    expect(instances[0].style.display).toBe("none");
  });

  it("rejects, revokes the object URL and detaches when play() fails", async () => {
    playImpl = () => Promise.reject(new Error("autoplay blocked"));

    await expect(playAudioBlob(new Uint8Array([1, 2]), "audio/wav")).rejects.toThrow(
      /autoplay blocked/,
    );
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
    expect(instances[0].isConnected).toBe(false);
  });

  it("revokes the object URL and detaches when playback reaches ended", async () => {
    await playAudioBlob(new Uint8Array([1, 2]), "audio/wav");
    expect(revokeObjectURL).not.toHaveBeenCalled();

    instances[0].dispatchEvent(new Event("ended"));
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
    expect(instances[0].isConnected).toBe(false);
  });

  it("revokes the object URL and detaches when the media element errors", async () => {
    await playAudioBlob(new Uint8Array([1, 2]), "audio/wav");

    instances[0].dispatchEvent(new Event("error"));
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
    expect(instances[0].isConnected).toBe(false);
  });
});
