import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import { PreviewVoiceResponseSchema, VoiceService } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { useVoicePreview } from "./voicePreview";

// One sample talks at a time on the whole page, not just within one hook: the
// agent editor stays mounted under the New NPC dialog, and each voice field and
// picker has its own useVoicePreview.
const players: { el: HTMLAudioElement; pause: ReturnType<typeof vi.fn> }[] = [];
function FakeAudio(src: string): HTMLAudioElement {
  const el = document.createElement("audio");
  el.src = src;
  const pause = vi.fn();
  Object.defineProperty(el, "play", { value: () => Promise.resolve(), configurable: true });
  Object.defineProperty(el, "pause", { value: pause, configurable: true });
  players.push({ el, pause });
  return el;
}

beforeEach(() => {
  players.length = 0;
  vi.stubGlobal("Audio", FakeAudio);
  (URL as unknown as Record<string, unknown>).createObjectURL = () => "blob:preview";
  (URL as unknown as Record<string, unknown>).revokeObjectURL = () => {};
});

afterEach(() => {
  vi.unstubAllGlobals();
  delete (URL as unknown as Record<string, unknown>).createObjectURL;
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
});

function Field({ name, voiceId }: { name: string; voiceId: string }) {
  const preview = useVoicePreview();
  return (
    <button type="button" onClick={() => preview.toggle(voiceId)}>
      {name}: {preview.phase}
    </button>
  );
}

function renderFields() {
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      previewVoice: () => create(PreviewVoiceResponseSchema, { audio: new Uint8Array([1]), mimeType: "audio/wav" }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Field name="editor" voiceId="rachel" />
      <Field name="dialog" voiceId="marcus" />
    </Providers>,
  );
}

describe("useVoicePreview", () => {
  it("stops another field's sample when a second field starts one", async () => {
    renderFields();
    fireEvent.click(screen.getByRole("button", { name: /^editor/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: /^editor/ })).toHaveTextContent("playing"));
    expect(players).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: /^dialog/ }));
    expect(players[0].pause).toHaveBeenCalled();
    expect(players[0].el.isConnected).toBe(false);
    expect(screen.getByRole("button", { name: /^editor/ })).toHaveTextContent("idle");
    await waitFor(() => expect(screen.getByRole("button", { name: /^dialog/ })).toHaveTextContent("playing"));
    expect(players).toHaveLength(2);
    expect(players[1].el.isConnected).toBe(true);
  });
});
