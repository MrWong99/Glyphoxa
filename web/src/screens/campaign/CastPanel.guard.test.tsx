import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignSchema,
  CampaignService,
  CreateAgentResponseSchema,
  GeneratePersonaResponseSchema,
  GetCampaignRosterResponseSchema,
  ListVoicesResponseSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { CastPanel } from "./CastPanel";

// W10: the agent editor's unsaved text was only guarded on a roster click. The
// Readiness segment and "Add NPC" (whose success selects the new NPC) unmounted
// or re-seeded it without a word, and a sidebar click had nothing to ask at all.
function mount(opts: { holdDraft?: boolean } = {}) {
  const roster = [
    create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true }),
    create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles." }),
    create(AgentSchema, { id: "n2", campaignId: "c1", role: "character", name: "Mira", persona: "Schemes." }),
  ];
  const created: string[] = [];
  const heldDrafts: (() => void)[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
          roster,
        }),
      createAgent: (req) => {
        created.push(req.name);
        const agent = create(AgentSchema, { id: `n${roster.length}`, campaignId: "c1", role: "character", name: req.name });
        roster.push(agent);
        return create(CreateAgentResponseSchema, { agent });
      },
      generatePersona: async () => {
        if (opts.holdDraft) await new Promise<void>((resolve) => heldDrafts.push(resolve));
        return create(GeneratePersonaResponseSchema, { persona: "A draft." });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CastPanel />
    </Providers>,
  );
  return { created, releaseDrafts: () => heldDrafts.splice(0).forEach((resolve) => resolve()) };
}

const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;
const rosterRow = (name: string) => screen.getByText(name).closest("button") as HTMLButtonElement;
const readiness = () => screen.getByRole("button", { name: /^Readiness/ });
const newNpcDialog = () => screen.queryByRole("dialog", { name: "New NPC" });

async function editBart() {
  await screen.findByText("Bart");
  fireEvent.click(rosterRow("Bart"));
  await waitFor(() => expect(persona().value).toBe("Grumbles."));
  fireEvent.change(persona(), { target: { value: "Grumbles, but secretly kind." } });
}

describe("CastPanel agent editor unsaved-edit guard (W10)", () => {
  it("asks before another roster member replaces the editor", async () => {
    mount();
    await editBart();
    fireEvent.click(rosterRow("Mira"));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(rosterRow("Mira"));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
  });

  it("asks before the Readiness view unmounts the editor", async () => {
    mount();
    await editBart();
    // Held from before the question opens: the page behind an open dialog is
    // hidden from the accessibility tree.
    const segment = readiness();
    fireEvent.click(segment);

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // Still on the editor while the question is open.
    expect(segment).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(readiness());
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(readiness()).toHaveAttribute("aria-pressed", "true"));
  });

  it("asks before Add NPC opens the dialog that will take the editor away", async () => {
    const { created } = mount();
    await editBart();
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // The New NPC dialog waits for the answer, and nothing is created.
    expect(newNpcDialog()).not.toBeInTheDocument();
    expect(created).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(newNpcDialog()).not.toBeInTheDocument();
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(newNpcDialog()).toBeInTheDocument());
    // Opening the dialog still creates nothing: only its submit does.
    expect(created).toEqual([]);
    const dialog = newNpcDialog() as HTMLElement;
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Odo" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create NPC" }));
    await waitFor(() => expect(created).toEqual(["Odo"]));
  });

  // "Discard" before the dialog must really discard: if the GM then cancels the
  // dialog, the editor may not still be showing text that nothing guards.
  it("lets go of the discarded edits even when the New NPC dialog is then cancelled", async () => {
    mount();
    await editBart();
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(newNpcDialog()).toBeInTheDocument());

    fireEvent.click(within(newNpcDialog() as HTMLElement).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(newNpcDialog()).not.toBeInTheDocument());

    // Bart's editor is back on what is stored, and the guard agrees.
    expect(persona().value).toBe("Grumbles.");
    expect(hasUnsavedEdits()).toBe(false);
    fireEvent.click(rosterRow("Mira"));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });

  it("opens the New NPC dialog at once when nothing was edited", async () => {
    mount();
    await screen.findByText("Bart");
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    expect(newNpcDialog()).toBeInTheDocument();
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });

  // A personality still being drafted is work that would be lost too: leaving
  // while it is on its way would drop it when it arrives.
  it("holds the guard while a personality draft is still being written", async () => {
    const { releaseDrafts } = mount({ holdDraft: true });
    await screen.findByText("Bart");
    fireEvent.click(rosterRow("Bart"));
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    fireEvent.change(screen.getByLabelText("Draft with AI"), { target: { value: "kinder" } });
    fireEvent.click(screen.getByRole("button", { name: /generate draft/i }));
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));

    fireEvent.click(rosterRow("Mira"));
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    releaseDrafts();
    await waitFor(() => expect(persona().value).toBe("A draft."));
    // The draft itself is an unsaved edit.
    expect(hasUnsavedEdits()).toBe(true);
  });

  it("reports the unsaved edit to the route layer, and clears it once the editor is gone", async () => {
    mount();
    await screen.findByText("Bart");
    expect(hasUnsavedEdits()).toBe(false);
    await editBart();
    // This flag is what NavigationGuard reads to hold a sidebar click.
    expect(hasUnsavedEdits()).toBe(true);

    fireEvent.click(readiness());
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));
  });

  it("clears the flag when the edit is reverted", async () => {
    mount();
    await editBart();
    expect(hasUnsavedEdits()).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Revert" }));
    expect(persona().value).toBe("Grumbles.");
    expect(hasUnsavedEdits()).toBe(false);
  });

  it("moves freely when nothing was edited", async () => {
    mount();
    await screen.findByText("Bart");
    fireEvent.click(rosterRow("Bart"));
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    fireEvent.click(rosterRow("Mira"));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });
});
