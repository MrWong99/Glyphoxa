import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignService,
  CreateAgentResponseSchema,
  ListVoicesResponseSchema,
  VoiceSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { NewNpcDialog } from "./NewNpcDialog";

type Created = { name: string; title: string; persona: string; voice: string; addressOnly: boolean; aliases: string[] };

function renderDialog(opts: { failCreate?: number } = {}) {
  const created: Created[] = [];
  const onCreated = vi.fn<(agent: Agent, concept: string) => void>();
  let failures = opts.failCreate ?? 0;
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      listVoices: () =>
        create(ListVoicesResponseSchema, {
          voices: [
            create(VoiceSchema, { voiceId: "rachel", name: "Rachel", language: "en", label: "ElevenLabs · Rachel" }),
            create(VoiceSchema, { voiceId: "marcus", name: "Marcus", language: "en", label: "ElevenLabs · Marcus" }),
          ],
        }),
    });
    service(CampaignService, {
      createAgent: (req) => {
        if (failures > 0) {
          failures -= 1;
          throw new ConnectError("roster store offline", Code.Unavailable);
        }
        created.push({
          name: req.name,
          title: req.title,
          persona: req.persona,
          voice: req.voice,
          addressOnly: req.addressOnly,
          aliases: [...req.aliases],
        });
        return create(CreateAgentResponseSchema, {
          agent: create(AgentSchema, {
            id: `npc-${created.length}`,
            role: "character",
            name: req.name,
            title: req.title,
            voice: req.voice,
          }),
        });
      },
    });
  });

  function Harness() {
    const voicesQuery = useQuery(VoiceService.method.listVoices, {});
    const [open, setOpen] = useState(true);
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          reopen
        </button>
        <NewNpcDialog
          open={open}
          onOpenChange={setOpen}
          voicesQuery={voicesQuery}
          campaignLanguage="en"
          onCreated={(agent, concept) => {
            onCreated(agent, concept);
            setOpen(false);
          }}
        />
      </>
    );
  }

  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Harness />
    </Providers>,
  );
  return { created, onCreated };
}

const dialog = () => screen.getByRole("dialog", { name: "New NPC" });
const field = (label: string | RegExp) => within(dialog()).getByLabelText(label) as HTMLInputElement;
const type = (label: string | RegExp, value: string) => fireEvent.change(field(label), { target: { value } });
const submit = () => fireEvent.click(within(dialog()).getByRole("button", { name: "Create NPC" }));

describe("NewNpcDialog", () => {
  it("asks who the NPC is, starting on the name", async () => {
    renderDialog();
    expect(dialog()).toBeInTheDocument();
    await waitFor(() => expect(field("Name")).toHaveFocus());
    expect(field("Name")).toHaveAttribute("aria-required", "true");
    // The optional answers are there, and say that they are optional.
    expect(field("Role shown under the name")).toBeInTheDocument();
    expect(field("Concept (optional)")).toBeInTheDocument();
    expect(within(dialog()).getByRole("button", { name: "Voice (optional)" })).toBeInTheDocument();
  });

  it("creates nothing without a name, and says what is missing", async () => {
    const { created, onCreated } = renderDialog();
    type("Name", "   ");
    submit();

    expect(await within(dialog()).findByText("Give the NPC a name.")).toBeInTheDocument();
    expect(field("Name")).toHaveAttribute("aria-invalid", "true");
    expect(field("Name")).toHaveFocus();
    expect(created).toEqual([]);
    expect(onCreated).not.toHaveBeenCalled();
  });

  it("sends CreateAgent with the real values and hands the new agent on", async () => {
    const { created, onCreated } = renderDialog();
    type("Name", "  Mira ");
    type("Role shown under the name", "Harbormaster");
    submit();

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    // The values the GM typed (trimmed) — no "New NPC" placeholder anywhere.
    expect(created).toEqual([
      { name: "Mira", title: "Harbormaster", persona: "", voice: "", addressOnly: false, aliases: [] },
    ]);
    const [agent, concept] = onCreated.mock.calls[0];
    expect(agent.id).toBe("npc-1");
    expect(agent.name).toBe("Mira");
    expect(concept).toBe("");
  });

  it("submits on Enter from a field", async () => {
    const { created } = renderDialog();
    type("Name", "Mira");
    fireEvent.submit(field("Name").closest("form") as HTMLFormElement);
    await waitFor(() => expect(created).toHaveLength(1));
  });

  it("hands the concept on for the editor to draft from; the dialog itself saves no personality", async () => {
    const { created, onCreated } = renderDialog();
    type("Name", "Mira");
    type("Concept (optional)", "  a stern harbourmaster who counts every crate  ");
    submit();

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(created[0].persona).toBe("");
    expect(onCreated.mock.calls[0][1]).toBe("a stern harbourmaster who counts every crate");
  });

  it("sends the voice picked in the dialog", async () => {
    const { created } = renderDialog();
    type("Name", "Mira");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Voice (optional)" }));
    fireEvent.click(await screen.findByText("ElevenLabs · Marcus"));
    submit();
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].voice).toBe("marcus");
  });

  // The voice picker opens OVER this dialog. Choosing there must come back to
  // the form with the voice set, the form's other answers intact, and must not
  // submit or close anything on the way.
  it("takes a voice from the picker opened inside the dialog", async () => {
    const { created, onCreated } = renderDialog();
    type("Name", "Mira");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Browse voices" }));

    const picker = await screen.findByRole("dialog", { name: "Choose a voice" });
    fireEvent.click(await within(picker).findByRole("button", { name: /^Marcus\b/ }));
    expect(created).toEqual([]);
    fireEvent.click(within(picker).getByRole("button", { name: "Use this voice" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose a voice" })).not.toBeInTheDocument());

    // Back on the form: still open, nothing created, the voice named.
    expect(onCreated).not.toHaveBeenCalled();
    expect(field("Name").value).toBe("Mira");
    expect(within(dialog()).getByText("ElevenLabs · Marcus")).toBeInTheDocument();

    submit();
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toMatchObject({ name: "Mira", voice: "marcus" });
  });

  it("closing the picker with Escape leaves the New NPC dialog open", async () => {
    renderDialog();
    type("Name", "Mira");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Browse voices" }));
    const picker = await screen.findByRole("dialog", { name: "Choose a voice" });
    fireEvent.keyDown(picker, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose a voice" })).not.toBeInTheDocument());
    expect(dialog()).toBeInTheDocument();
    expect(field("Name").value).toBe("Mira");
  });

  it("stays open with the GM's answers when the create fails, and can be retried", async () => {
    const { created, onCreated } = renderDialog({ failCreate: 1 });
    type("Name", "Mira");
    type("Role shown under the name", "Harbormaster");
    submit();

    const alert = await within(dialog()).findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't create the NPC");
    expect(alert).toHaveTextContent(/roster store offline/);
    expect(onCreated).not.toHaveBeenCalled();
    expect(field("Name").value).toBe("Mira");
    expect(field("Role shown under the name").value).toBe("Harbormaster");

    submit();
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(created).toHaveLength(1);
  });

  it("opens as a fresh form every time", async () => {
    renderDialog();
    type("Name", "Mira");
    type("Concept (optional)", "a stern harbourmaster");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "reopen" }));
    expect(field("Name").value).toBe("");
    expect(field("Concept (optional)").value).toBe("");
  });

  it("cancelling creates nothing", async () => {
    const { created, onCreated } = renderDialog();
    type("Name", "Mira");
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(created).toEqual([]);
    expect(onCreated).not.toHaveBeenCalled();
  });
});
