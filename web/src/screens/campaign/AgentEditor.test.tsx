import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignService,
  GeneratePersonaResponseSchema,
  GraphNodeSchema,
  ListToolGrantsResponseSchema,
  ListVoicesResponseSchema,
  NodeType,
  ToolGrantSchema,
  UpdateAgentResponseSchema,
  VoiceSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { AgentEditor } from "./AgentEditor";
import { rosterEntries, type Readiness } from "./rosterPrep";

type AgentInit = Parameters<typeof create<typeof AgentSchema>>[1];
type Update = {
  id: string;
  name: string;
  title: string;
  persona: string;
  voice: string;
  addressOnly: boolean;
  aliases: string[];
};

const MIRA: AgentInit = {
  id: "n1",
  campaignId: "c1",
  role: "character",
  name: "Mira",
  title: "Harbormaster",
  persona: "Counts every crate.",
  voice: "rachel",
  aliases: ["the Harbormaster"],
};

// The editor on its own: a store of ONE agent behind UpdateAgent, and a parent
// that hands the stored agent back after a save the way the roster re-read does.
function renderEditor(
  opts: {
    agent?: AgentInit;
    /** World state to grade the agent against; omitted, the editor gets no readiness. */
    world?: { nodes: GraphNode[]; readiness?: Readiness };
    autoDraft?: string;
    failSave?: boolean;
    /** Hold UpdateAgent until released. */
    holdSave?: boolean;
    /** Leave the agent prop stale after a save (the roster re-read has not landed). */
    noReread?: boolean;
  } = {},
) {
  let stored = create(AgentSchema, opts.agent ?? MIRA);
  const updates: Update[] = [];
  const drafts: { agentId: string; prompt: string; name?: string; title?: string }[] = [];
  const held: (() => void)[] = [];
  const onDirtyChange = vi.fn();
  const onAutoDraftStarted = vi.fn();
  const onOpenNode = vi.fn();
  const onOpenWiki = vi.fn();
  const onDelete = vi.fn();

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
      updateAgent: async (req) => {
        updates.push({
          id: req.id,
          name: req.name,
          title: req.title,
          persona: req.persona,
          voice: req.voice,
          addressOnly: req.addressOnly,
          aliases: [...req.aliases],
        });
        if (opts.holdSave) await new Promise<void>((resolve) => held.push(resolve));
        if (opts.failSave) throw new Error("boom");
        stored = create(AgentSchema, {
          ...stored,
          name: req.name,
          title: req.title,
          persona: req.persona,
          voice: req.voice,
          addressOnly: stored.role === "butler" ? true : req.addressOnly,
          aliases: [...req.aliases],
        });
        return create(UpdateAgentResponseSchema, { agent: stored });
      },
      generatePersona: (req) => {
        drafts.push({ agentId: req.agentId, prompt: req.prompt, name: req.name, title: req.title });
        return create(GeneratePersonaResponseSchema, { persona: "**You are Mira**, stern and exact." });
      },
      listToolGrants: () =>
        create(ListToolGrantsResponseSchema, {
          grants: [create(ToolGrantSchema, { toolName: "dice", description: "Roll dice.", granted: false })],
        }),
    });
  });

  function Harness() {
    const voicesQuery = useQuery(VoiceService.method.listVoices, {});
    const [agent, setAgent] = useState<Agent>(stored);
    const prep = opts.world
      ? rosterEntries(
          [agent],
          opts.world.nodes,
          opts.world.readiness ? new Map([[agent.id, opts.world.readiness]]) : new Map(),
        )[0]
      : undefined;
    return (
      // The editor's styles hang off the Cast panel's root.
      <div className="gx-cast">
        <AgentEditor
          agent={agent}
          voicesQuery={voicesQuery}
          campaignLanguage="en"
          prep={prep}
          onSaved={() => {
            if (!opts.noReread) setAgent(stored);
          }}
          onDelete={agent.role === "butler" ? undefined : onDelete}
          deleting={false}
          onDirtyChange={onDirtyChange}
          autoDraft={opts.autoDraft}
          onAutoDraftStarted={onAutoDraftStarted}
          onOpenNode={onOpenNode}
          onOpenWiki={onOpenWiki}
        />
      </div>
    );
  }

  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Harness />
    </Providers>,
  );
  return {
    updates,
    drafts,
    onDirtyChange,
    onAutoDraftStarted,
    onOpenNode,
    onOpenWiki,
    release: () => held.splice(0).forEach((resolve) => resolve()),
  };
}

const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;
const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;
const aliasField = () => screen.getByRole("textbox", { name: "Also answers to" }) as HTMLInputElement;
const aliasChips = () =>
  Array.from(document.querySelectorAll(".gx-chip__label")).map((el) => el.textContent);
const saveButton = () => screen.getByRole("button", { name: "Save changes" });
const revertButton = () => screen.getByRole("button", { name: "Revert" });
const pressSaveChord = (init: KeyboardEventInit = { ctrlKey: true }) =>
  fireEvent.keyDown(document.activeElement ?? document.body, { key: "s", ...init });

function addAlias(value: string) {
  fireEvent.change(aliasField(), { target: { value } });
  fireEvent.keyDown(aliasField(), { key: "Enter" });
}

describe("AgentEditor save state", () => {
  it("starts clean: Save and Revert are disabled and nothing claims to be unsaved", () => {
    const { onDirtyChange } = renderEditor();
    expect(saveButton()).toBeDisabled();
    expect(revertButton()).toBeDisabled();
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("marks the form unsaved on the first edit and reports it to the guard", () => {
    const { onDirtyChange } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(saveButton()).toBeEnabled();
    expect(revertButton()).toBeEnabled();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Typing the stored value back is clean again: dirtiness is derived.
    fireEvent.change(nameField(), { target: { value: "Mira" } });
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  it("saves what the form holds and shows Saved only once the form is what was stored", async () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    fireEvent.click(saveButton());

    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(updates).toEqual([
      {
        id: "n1",
        name: "Mira Vance",
        title: "Harbormaster",
        persona: "Counts every crate.",
        voice: "rachel",
        addressOnly: false,
        aliases: ["the Harbormaster"],
      },
    ]);
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();

    // The next edit takes "Saved" away again: it is no longer true.
    fireEvent.change(nameField(), { target: { value: "Mira V." } });
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
  });

  // Between the save returning and the roster re-read, the agent prop is still
  // the OLD agent. The form must not read "Unsaved changes" for that moment.
  it("reads Saved straight after the save, before the roster has been re-read", async () => {
    renderEditor({ noReread: true });
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    fireEvent.click(saveButton());
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(nameField().value).toBe("Mira Vance");
  });

  it("is busy and cannot be double-submitted while the save is in flight", async () => {
    const { updates, release } = renderEditor({ holdSave: true });
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    fireEvent.click(saveButton());

    await waitFor(() => expect(saveButton()).toHaveAttribute("aria-busy", "true"));
    expect(saveButton()).toBeDisabled();
    fireEvent.click(saveButton());
    pressSaveChord();
    expect(updates).toHaveLength(1);

    release();
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("keeps a failed save on screen, with the GM's text still unsaved", async () => {
    renderEditor({ failSave: true });
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    fireEvent.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save/i);
    expect(nameField().value).toBe("Mira Vance");
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
    // Still dirty, so it can be tried again.
    expect(saveButton()).toBeEnabled();
  });

  it("refuses to save an empty name, and says why", () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "   " } });
    expect(screen.getByText("A name is required.")).toBeInTheDocument();
    expect(nameField()).toHaveAttribute("aria-invalid", "true");
    expect(saveButton()).toBeDisabled();
    pressSaveChord();
    expect(updates).toEqual([]);
  });

  it("sends the name the server will store, so a trailing space does not leave the form dirty", async () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Mira Vance  " } });
    fireEvent.click(saveButton());
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(updates[0].name).toBe("Mira Vance");
    expect(nameField().value).toBe("Mira Vance");
  });
});

describe("AgentEditor keyboard and revert", () => {
  it("saves on Ctrl+S and on Cmd+S", async () => {
    const { updates } = renderEditor();
    fireEvent.change(persona(), { target: { value: "Counts every crate, twice." } });
    persona().focus();
    pressSaveChord({ ctrlKey: true });
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(updates).toHaveLength(1);
    expect(updates[0].persona).toBe("Counts every crate, twice.");

    fireEvent.change(persona(), { target: { value: "Counts every crate, three times." } });
    pressSaveChord({ metaKey: true });
    await waitFor(() => expect(updates).toHaveLength(2));
  });

  it("swallows the chord when there is nothing to save, without calling the server", () => {
    const { updates } = renderEditor();
    const event = new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true, cancelable: true });
    document.body.dispatchEvent(event);
    // The browser's "save page" dialog is suppressed either way.
    expect(event.defaultPrevented).toBe(true);
    expect(updates).toEqual([]);
  });

  it("leaves a plain 's' and other chords alone", () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    const plain = new KeyboardEvent("keydown", { key: "s", bubbles: true, cancelable: true });
    document.body.dispatchEvent(plain);
    const other = new KeyboardEvent("keydown", { key: "s", ctrlKey: true, altKey: true, bubbles: true, cancelable: true });
    document.body.dispatchEvent(other);
    expect(plain.defaultPrevented).toBe(false);
    expect(other.defaultPrevented).toBe(false);
    expect(updates).toEqual([]);
  });

  // The chord belongs to the form, not to a dialog layered over it.
  it("does not save from inside the voice picker", async () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Mira Vance" } });
    fireEvent.click(screen.getByRole("button", { name: "Browse voices" }));
    const picker = await screen.findByRole("dialog", { name: "Choose a voice" });
    fireEvent.keyDown(await within(picker).findByLabelText("Sample line"), { key: "s", ctrlKey: true });
    expect(updates).toEqual([]);
  });

  it("Revert restores every field to the agent's stored values", () => {
    const { updates } = renderEditor();
    fireEvent.change(nameField(), { target: { value: "Someone Else" } });
    fireEvent.change(screen.getByLabelText("Role shown under the name"), { target: { value: "Pirate" } });
    fireEvent.change(persona(), { target: { value: "Arr." } });
    fireEvent.click(screen.getByLabelText("Only answers when called by name"));
    addAlias("Meera");
    fireEvent.change(aliasField(), { target: { value: "half-typed" } });
    expect(aliasChips()).toEqual(["the Harbormaster", "Meera"]);

    fireEvent.click(revertButton());

    expect(nameField().value).toBe("Mira");
    expect((screen.getByLabelText("Role shown under the name") as HTMLInputElement).value).toBe("Harbormaster");
    expect(persona().value).toBe("Counts every crate.");
    expect(screen.getByLabelText("Only answers when called by name")).not.toBeChecked();
    expect(aliasChips()).toEqual(["the Harbormaster"]);
    expect(aliasField().value).toBe("");
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    expect(updates).toEqual([]);
  });
});

describe("AgentEditor aliases", () => {
  it("shows the stored aliases with a hint about nicknames and speech-to-text spellings", () => {
    renderEditor();
    expect(aliasChips()).toEqual(["the Harbormaster"]);
    expect(aliasField()).toHaveAccessibleDescription(/nicknames.*speech-to-text/i);
  });

  it("adding 'Meera' marks the editor dirty and Save sends the aliases including it", async () => {
    const { updates, onDirtyChange } = renderEditor();
    expect(saveButton()).toBeDisabled();

    addAlias("Meera");
    expect(aliasChips()).toEqual(["the Harbormaster", "Meera"]);
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    fireEvent.click(saveButton());
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(updates[0].aliases).toEqual(["the Harbormaster", "Meera"]);
  });

  it("removing an alias is an edit too", async () => {
    const { updates } = renderEditor();
    fireEvent.click(screen.getByRole("button", { name: "Remove the Harbormaster" }));
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    fireEvent.click(saveButton());
    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0].aliases).toEqual([]);
  });

  // Ctrl+S never blurs the field, so text that is typed but not yet a chip
  // would be lost exactly when the GM thinks they saved it.
  it("counts a half-typed alias as an edit and Ctrl+S takes it along", async () => {
    const { updates } = renderEditor();
    aliasField().focus();
    fireEvent.change(aliasField(), { target: { value: "Meera" } });
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();

    pressSaveChord();
    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0].aliases).toEqual(["the Harbormaster", "Meera"]);
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(aliasChips()).toEqual(["the Harbormaster", "Meera"]);
    // The word became a chip; it does not also still sit in the field, and
    // the GM is still in the field to type the next one.
    expect(aliasField()).toHaveValue("");
    expect(aliasField()).toHaveFocus();
  });

  it("offers aliases for the Butler too, and keeps it Address-Only", async () => {
    const { updates } = renderEditor({
      agent: { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true },
    });
    const addressOnly = screen.getByLabelText("Only answers when called by name");
    expect(addressOnly).toBeChecked();
    expect(addressOnly).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Delete NPC" })).not.toBeInTheDocument();

    addAlias("Glyph");
    fireEvent.click(saveButton());
    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0].aliases).toEqual(["Glyph"]);
    expect(updates[0].addressOnly).toBe(true);
  });
});

describe("AgentEditor layout of the form", () => {
  it("has the addressing switch with the name fields, not behind the advanced card", () => {
    renderEditor();
    // Reachable without opening anything.
    expect(screen.getByLabelText("Only answers when called by name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /advanced: tools/i })).toHaveAttribute("aria-expanded", "false");
  });

  it("says that tool grants apply immediately, unlike the rest of the form", async () => {
    const { updates } = renderEditor();
    fireEvent.click(screen.getByRole("button", { name: /advanced: tools/i }));
    expect(screen.getByText(/changes apply immediately, from the next session/i)).toBeInTheDocument();
    // A grant is not a form edit: it never makes the form dirty.
    await screen.findByLabelText("Dice rolls");
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(updates).toEqual([]);
  });

  it("picks a voice through the picker as an unsaved edit", async () => {
    const { updates } = renderEditor();
    fireEvent.click(screen.getByRole("button", { name: "Browse voices" }));
    const picker = await screen.findByRole("dialog", { name: "Choose a voice" });
    fireEvent.click(await within(picker).findByRole("button", { name: /^Marcus\b/ }));
    fireEvent.click(within(picker).getByRole("button", { name: "Use this voice" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByText("ElevenLabs · Marcus")).toBeInTheDocument();
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    fireEvent.click(saveButton());
    await waitFor(() => expect(updates).toHaveLength(1));
    expect(updates[0].voice).toBe("marcus");
  });
});

describe("AgentEditor readiness callout", () => {
  const blank: AgentInit = { id: "n1", campaignId: "c1", role: "character", name: "Mira" };
  const entry = (init: Parameters<typeof create<typeof GraphNodeSchema>>[1]) =>
    create(GraphNodeSchema, { nodeType: NodeType.NPC, ...init });
  const facts = (o: Partial<Readiness> = {}): Readiness => ({
    linked: true,
    factCount: 3,
    chars: 400,
    maxChars: 4000,
    truncated: false,
    ...o,
  });
  const callout = () => screen.getByRole("note");
  const item = (name: string) => within(callout()).getByRole("button", { name });

  it("is absent for an NPC that is ready, and for one the editor has no readiness for", () => {
    renderEditor({
      world: { nodes: [entry({ id: "mira", name: "Mira", agentId: "n1", bodyLen: 40 })], readiness: facts() },
    });
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("is absent for the Butler, which is not graded", () => {
    renderEditor({
      agent: { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true },
      world: { nodes: [] },
    });
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("names everything that is missing, each as a way to fix it", () => {
    renderEditor({ agent: blank, world: { nodes: [] } });
    expect(callout()).toHaveTextContent("Mira is not session-ready yet");
    expect(
      within(within(callout()).getByRole("list", { name: "What is missing" }))
        .getAllByRole("button")
        .map((b) => b.textContent),
    ).toEqual([
      "No personality written",
      "No voice",
      "No wiki entry linked",
      "Wiki entry is empty",
      "No facts in reach",
    ]);
  });

  it("jumps to the personality field, and drops the item once something is written", () => {
    renderEditor({ agent: blank, world: { nodes: [] } });
    fireEvent.click(item("No personality written"));
    expect(persona()).toHaveFocus();

    fireEvent.change(persona(), { target: { value: "Stern." } });
    expect(within(callout()).queryByRole("button", { name: "No personality written" })).not.toBeInTheDocument();
    // The rest is still missing.
    expect(item("No voice")).toBeInTheDocument();
  });

  it("opens the voice picker from 'No voice'", async () => {
    renderEditor({ agent: blank, world: { nodes: [] } });
    fireEvent.click(item("No voice"));
    expect(await screen.findByRole("dialog", { name: "Choose a voice" })).toBeInTheDocument();
  });

  it("opens the NPC's own wiki entry for the wiki-side checks", () => {
    const { onOpenNode, onOpenWiki } = renderEditor({
      agent: { ...blank, persona: "Stern.", voice: "rachel" },
      world: { nodes: [entry({ id: "mira-entry", name: "Mira", agentId: "n1", bodyLen: 0 })], readiness: facts({ factCount: 0 }) },
    });
    fireEvent.click(item("Wiki entry is empty"));
    expect(onOpenNode).toHaveBeenLastCalledWith("mira-entry");
    fireEvent.click(item("No facts in reach"));
    expect(onOpenNode).toHaveBeenCalledTimes(2);
    expect(onOpenWiki).not.toHaveBeenCalled();
  });

  it("opens the same-named unlinked entry from 'No wiki entry linked', else the World wiki", () => {
    const withCandidate = renderEditor({
      agent: { ...blank, persona: "Stern.", voice: "rachel" },
      world: { nodes: [entry({ id: "loose", name: "Mira" })] },
    });
    fireEvent.click(item("No wiki entry linked"));
    expect(withCandidate.onOpenNode).toHaveBeenCalledWith("loose");
    expect(withCandidate.onOpenWiki).not.toHaveBeenCalled();
  });

  it("falls back to the World wiki when there is no entry to open", () => {
    const { onOpenNode, onOpenWiki } = renderEditor({
      agent: { ...blank, persona: "Stern.", voice: "rachel" },
      world: { nodes: [] },
    });
    fireEvent.click(item("No wiki entry linked"));
    expect(onOpenWiki).toHaveBeenCalled();
    expect(onOpenNode).not.toHaveBeenCalled();
  });

  it("warns when the NPC's knowledge was trimmed, even if nothing is missing", () => {
    renderEditor({
      agent: { ...blank, persona: "Stern.", voice: "rachel" },
      world: {
        nodes: [entry({ id: "mira", name: "Mira", agentId: "n1", bodyLen: 40 })],
        readiness: facts({ truncated: true, chars: 4000 }),
      },
    });
    expect(callout()).toHaveTextContent("Knowledge trimmed 4000/4000");
    expect(callout()).toHaveTextContent(/more than fits into one reply/i);
    expect(within(callout()).queryByRole("list")).not.toBeInTheDocument();
  });
});

describe("AgentEditor personality draft", () => {
  it("drafts from the New NPC concept once, as an unsaved draft", async () => {
    const { drafts, updates, onAutoDraftStarted, onDirtyChange } = renderEditor({
      agent: { id: "n1", campaignId: "c1", role: "character", name: "Mira", title: "Harbormaster" },
      autoDraft: "a stern harbourmaster",
    });

    // Started without a press, exactly once, with the NPC's real name and role.
    await waitFor(() => expect(drafts).toHaveLength(1));
    expect(drafts[0]).toEqual({ agentId: "n1", prompt: "a stern harbourmaster", name: "Mira", title: "Harbormaster" });
    expect(onAutoDraftStarted).toHaveBeenCalledTimes(1);

    // The draft is in the field for review, and nothing was saved.
    await waitFor(() => expect(persona().value).toBe("**You are Mira**, stern and exact."));
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    expect(updates).toEqual([]);
    // The concept stays in the prompt field, ready for another go.
    expect((screen.getByLabelText("Draft with AI") as HTMLInputElement).value).toBe("a stern harbourmaster");
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(drafts).toHaveLength(1);
  });

  it("drafts on demand with the live (unsaved) name and title (#480)", async () => {
    const { drafts } = renderEditor({
      agent: { id: "n1", campaignId: "c1", role: "character", name: "Placeholder", title: "Stored Title" },
    });
    fireEvent.change(nameField(), { target: { value: "Jule Brandt" } });
    fireEvent.change(screen.getByLabelText("Role shown under the name"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Draft with AI"), { target: { value: "a stern harbourmaster" } });
    fireEvent.click(screen.getByRole("button", { name: /generate draft/i }));

    await waitFor(() => expect(drafts).toHaveLength(1));
    // The cleared title rides along as empty, telling the server NOT to fall
    // back to the stored one.
    expect(drafts[0]).toEqual({ agentId: "n1", prompt: "a stern harbourmaster", name: "Jule Brandt", title: "" });
    await waitFor(() => expect(persona().value).toBe("**You are Mira**, stern and exact."));
  });

  it("offers no AI draft for the Butler", () => {
    renderEditor({ agent: { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true } });
    expect(screen.queryByLabelText("Draft with AI")).not.toBeInTheDocument();
  });
});
