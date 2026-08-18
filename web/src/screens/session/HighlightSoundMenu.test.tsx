import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
  Toaster: () => null,
}));

import { toast } from "sonner";

import {
  SessionService,
  HighlightSchema,
  ListHighlightsResponseSchema,
  SetHighlightSoundResponseSchema,
  type Highlight,
  type SetHighlightSoundRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { HighlightsStrip } from "./HighlightsStrip";

type HighlightInit = MessageInitShape<typeof HighlightSchema>;

function promoted(over: HighlightInit = {}): Highlight {
  return create(HighlightSchema, {
    id: "h2",
    voiceSessionId: "vs1",
    status: "promoted",
    startsAt: timestampFromDate(new Date("2026-07-11T20:15:30Z")),
    endsAt: timestampFromDate(new Date("2026-07-11T20:16:10Z")),
    score: 8.5,
    excerpt: "And then the dragon spoke my true name.",
    reason: "Dramatic reveal — party fell silent.",
    clipContentType: "audio/wav",
    clipSizeBytes: 12345n,
    ...over,
  });
}

// An in-memory transport whose setHighlightSound records the request, mutates
// the closure the ShareHighlightDialog-style expand group refetches, and can
// fail with the server's unconfigured FailedPrecondition.
function soundTransport(seed: Highlight[], opts: { failSound?: boolean } = {}) {
  let highlights = [...seed];
  const soundCalls: SetHighlightSoundRequest[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights }),
      setHighlightSound: (req) => {
        soundCalls.push(req);
        if (opts.failSound) {
          throw new ConnectError(
            "sound generation requires an ElevenLabs tts provider configuration",
            Code.FailedPrecondition,
          );
        }
        highlights = highlights.map((h) =>
          h.id === req.id
            ? create(HighlightSchema, {
                ...h,
                soundKind: req.kind,
                soundContentType: "",
                soundSizeBytes: 0n,
                soundRequestedAt:
                  req.kind === "" ? undefined : timestampFromDate(new Date()),
              })
            : h,
        );
        return create(SetHighlightSoundResponseSchema, {
          highlight: highlights.find((h) => h.id === req.id),
        });
      },
    });
  });
  return { transport, soundCalls };
}

function renderStrip(seed: Highlight[], opts?: { failSound?: boolean }) {
  const ctx = soundTransport(seed, opts);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <HighlightsStrip sessionId="vs1" live={false} />
    </Providers>,
  );
  return ctx;
}

describe("HighlightSoundMenu (#312)", () => {
  it("requests a sting through SetHighlightSound and flips the row to the pending state", async () => {
    const ctx = renderStrip([promoted()]);
    fireEvent.click(await screen.findByRole("button", { name: /add sound/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^sting$/i }));

    await waitFor(() => expect(ctx.soundCalls).toHaveLength(1));
    expect(ctx.soundCalls[0].id).toBe("h2");
    expect(ctx.soundCalls[0].kind).toBe("sting");
    expect(toast.success).toHaveBeenCalled();
    // The invalidated list refetch lands the requested-but-unlanded state.
    expect(await screen.findByTestId("sound-pending")).toBeInTheDocument();
  });

  it("renders the generated sound as a second audio element once it lands", async () => {
    renderStrip([
      promoted({
        soundKind: "music",
        soundContentType: "audio/mpeg",
        soundSizeBytes: 999n,
        soundRequestedAt: timestampFromDate(new Date()),
      }),
    ]);
    await screen.findByText(/the dragon spoke my true name/i);
    const audios = document.querySelectorAll("audio");
    expect(audios).toHaveLength(2);
    expect(audios[1]).toHaveAttribute("src", "/api/v1/highlights/h2/sound");
    // Landed: no pending note.
    expect(screen.queryByTestId("sound-pending")).toBeNull();
  });

  it("offers Remove only once a choice exists, and clears it via kind \"\"", async () => {
    const ctx = renderStrip([
      promoted({
        soundKind: "sting",
        soundContentType: "audio/mpeg",
        soundRequestedAt: timestampFromDate(new Date()),
      }),
    ]);
    // The collapsed button names the standing choice.
    fireEvent.click(await screen.findByRole("button", { name: /sound: sting/i }));
    fireEvent.click(await screen.findByRole("button", { name: /remove sound/i }));

    await waitFor(() => expect(ctx.soundCalls).toHaveLength(1));
    expect(ctx.soundCalls[0].kind).toBe("");
    // After the refetch the choice is gone: the action reads Add sound again.
    expect(await screen.findByRole("button", { name: /add sound/i })).toBeInTheDocument();
  });

  it("does not render the sound action on candidates (sound is opt-in after promotion)", async () => {
    renderStrip([promoted({ id: "h1", status: "candidate" })]);
    await screen.findByText(/the dragon spoke my true name/i);
    expect(screen.queryByRole("button", { name: /add sound/i })).toBeNull();
  });

  it("surfaces the server's unconfigured refusal as an error toast", async () => {
    renderStrip([promoted()], { failSound: true });
    fireEvent.click(await screen.findByRole("button", { name: /add sound/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^music$/i }));

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(String(vi.mocked(toast.error).mock.calls.at(-1)?.[0])).toMatch(/ElevenLabs/i);
  });
});
