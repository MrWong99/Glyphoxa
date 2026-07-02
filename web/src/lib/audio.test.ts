import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { playAudioBlob } from "./audio";

// jsdom has no real media pipeline: Audio.play is unimplemented and
// URL.createObjectURL doesn't exist. These stubs stand in so the tests can
// observe the object-URL lifecycle (#154 item 3: a failed preview must revoke
// its URL and surface the play() rejection instead of leaking silently).
class FakeAudio {
  static instances: FakeAudio[] = [];
  static playImpl: () => Promise<void> = () => Promise.resolve();
  src: string;
  private listeners = new Map<string, Array<() => void>>();
  constructor(src: string) {
    this.src = src;
    FakeAudio.instances.push(this);
  }
  addEventListener(type: string, fn: () => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  dispatch(type: string) {
    for (const fn of this.listeners.get(type) ?? []) fn();
  }
  play() {
    return FakeAudio.playImpl();
  }
}

const createObjectURL = vi.fn(() => "blob:preview");
const revokeObjectURL = vi.fn();

beforeEach(() => {
  FakeAudio.instances = [];
  FakeAudio.playImpl = () => Promise.resolve();
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
});

describe("playAudioBlob", () => {
  it("rejects and revokes the object URL when play() fails", async () => {
    FakeAudio.playImpl = () => Promise.reject(new Error("autoplay blocked"));

    await expect(playAudioBlob(new Uint8Array([1, 2]), "audio/wav")).rejects.toThrow(
      /autoplay blocked/,
    );
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });

  it("still revokes the object URL when playback reaches ended", async () => {
    await playAudioBlob(new Uint8Array([1, 2]), "audio/wav");
    expect(revokeObjectURL).not.toHaveBeenCalled();

    FakeAudio.instances[0].dispatch("ended");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });

  it("revokes the object URL when the media element errors", async () => {
    await playAudioBlob(new Uint8Array([1, 2]), "audio/wav");

    FakeAudio.instances[0].dispatch("error");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview");
  });
});
