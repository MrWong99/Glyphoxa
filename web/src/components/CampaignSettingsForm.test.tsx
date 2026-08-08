import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  CampaignSchema,
  ListSupportedLanguagesResponseSchema,
  UpdateCampaignResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignSettingsForm } from "./CampaignSettingsForm";

type Camp = { id?: string; name?: string; system?: string; language?: string; tapeArmed?: boolean };

// A Connect backend for the settings form: ListSupportedLanguages returns the
// registered encoder codes (the Campaign Language choices, ADR-0024) and
// UpdateCampaign echoes the request so a test can assert exactly what rode the
// wire. `updated` captures the last UpdateCampaign request.
function mockBackend(
  opts: {
    languages?: string[];
    languagesError?: string;
    updated?: { req?: Record<string, unknown> };
    updateError?: string;
  } = {},
) {
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listSupportedLanguages: () => {
        if (opts.languagesError) throw new ConnectError(opts.languagesError, Code.Unavailable);
        return create(ListSupportedLanguagesResponseSchema, { languages: opts.languages ?? ["de", "en"] });
      },
      updateCampaign: (req) => {
        if (opts.updated)
          opts.updated.req = {
            id: req.id,
            name: req.name,
            system: req.system,
            language: req.language,
            tapeArmed: req.tapeArmed,
          };
        return create(UpdateCampaignResponseSchema, {
          campaign: create(CampaignSchema, {
            id: req.id,
            name: req.name,
            system: req.system,
            language: req.language,
            tapeArmed: req.tapeArmed ?? false,
          }),
        });
      },
    });
  });
}

function makeCampaign(c: Camp = {}) {
  return create(CampaignSchema, {
    id: c.id ?? "camp-1",
    name: c.name ?? "The Sunless Citadel",
    system: c.system ?? "D&D 5e",
    language: c.language ?? "en",
    tapeArmed: c.tapeArmed ?? false,
  });
}

function renderForm(
  props: { campaign?: ReturnType<typeof makeCampaign>; onSaved?: () => void; onCancel?: () => void } = {},
  transport = mockBackend(),
) {
  const onSaved = props.onSaved ?? vi.fn();
  const onCancel = props.onCancel ?? vi.fn();
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CampaignSettingsForm campaign={props.campaign ?? makeCampaign()} onSaved={onSaved} onCancel={onCancel} />
    </Providers>,
  );
  return { onSaved, onCancel };
}

// openLanguageSelect opens the Radix language Select and returns the listbox.
async function openLanguageSelect() {
  fireEvent.keyDown(screen.getByRole("combobox", { name: /language/i }), { key: "Enter" });
  return screen.getByRole("listbox");
}

// The Highlight-recording switch lives behind the closed-by-default
// AdvancedCard, whose body isn't mounted until the disclosure is opened.
const openAdvanced = () =>
  fireEvent.click(screen.getByRole("button", { name: /advanced settings/i }));

describe("CampaignSettingsForm", () => {
  it("prefills the fields from the campaign prop and shows the Voice Session hint", () => {
    renderForm({ campaign: makeCampaign({ name: "Curse of Strahd", system: "D&D 5e", language: "en" }) });

    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Curse of Strahd");
    expect((screen.getByLabelText("Game system") as HTMLInputElement).value).toBe("D&D 5e");
    // The language change deferral notice (mutates nothing now; next voice session).
    expect(screen.getByText(/next voice session/i)).toBeInTheDocument();
  });

  it("offers the registered languages as the language options", async () => {
    renderForm({ campaign: makeCampaign({ language: "en" }) }, mockBackend({ languages: ["de", "en"] }));

    const list = await openLanguageSelect();
    // Options are labeled "<English name> (<code>)" — zero hardcoded list. They
    // arrive async (ListSupportedLanguages resolves after open), so findByRole.
    expect(await within(list).findByRole("option", { name: /German \(de\)/ })).toBeInTheDocument();
    expect(within(list).getByRole("option", { name: /English \(en\)/ })).toBeInTheDocument();
  });

  it("preserves a stored out-of-registry language as an extra option", async () => {
    renderForm({ campaign: makeCampaign({ language: "fr" }) }, mockBackend({ languages: ["de", "en"] }));

    // fr has no registered encoder, but it must stay selectable so a save can't
    // silently coerce it to a registered language.
    const list = await openLanguageSelect();
    expect(await within(list).findByRole("option", { name: /unsupported/i })).toBeInTheDocument();
  });

  it("follows the mocked registry, not a hardcoded language list", async () => {
    // A DISTINCT set the shipped registry never returns: a regression that
    // hardcodes en/de (and never calls the RPC) would show "de" and miss "xx",
    // so this pins the SELECT to the RPC answer (ADR-0024, sole truth source).
    renderForm({ campaign: makeCampaign({ language: "en" }) }, mockBackend({ languages: ["en", "xx"] }));

    const list = await openLanguageSelect();
    expect(await within(list).findByRole("option", { name: "xx" })).toBeInTheDocument();
    expect(within(list).getByRole("option", { name: /English \(en\)/ })).toBeInTheDocument();
    // "de" is registered by the real platform but NOT by this mock — it must be absent.
    expect(within(list).queryByRole("option", { name: /German \(de\)/ })).not.toBeInTheDocument();
  });

  it("does not mislabel a stored registered language while the registry is still loading", () => {
    // Open the SELECT synchronously, before ListSupportedLanguages resolves:
    // supported is empty for lack of an answer, not because "en" is unregistered,
    // so the stored value must ride as a plain option — never a false "unsupported".
    renderForm({ campaign: makeCampaign({ language: "en" }) });
    fireEvent.keyDown(screen.getByRole("combobox", { name: /language/i }), { key: "Enter" });
    const list = screen.getByRole("listbox");
    expect(within(list).queryByRole("option", { name: /unsupported/i })).not.toBeInTheDocument();
  });

  it("surfaces a registry load failure instead of mislabeling the stored language", async () => {
    renderForm(
      { campaign: makeCampaign({ language: "en" }) },
      mockBackend({ languagesError: "registry offline" }),
    );

    // The error is shown (not swallowed)…
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't load the language choices/i);
    expect(alert).toHaveTextContent(/registry offline/);
    // …and the stored value is not falsely branded unsupported on a failed load.
    fireEvent.keyDown(screen.getByRole("combobox", { name: /language/i }), { key: "Enter" });
    const list = screen.getByRole("listbox");
    expect(within(list).queryByRole("option", { name: /unsupported/i })).not.toBeInTheDocument();
  });

  it("suggests three systems via a datalist", () => {
    renderForm();
    const input = screen.getByLabelText("Game system") as HTMLInputElement;
    const listId = input.getAttribute("list");
    expect(listId).toBeTruthy();
    const datalist = document.getElementById(listId!);
    const values = Array.from(datalist!.querySelectorAll("option")).map((o) => o.getAttribute("value"));
    expect(values).toEqual(["D&D 5e", "Pathfinder 2e", "Call of Cthulhu"]);
  });

  it("disables save when the name is empty or whitespace", () => {
    renderForm();
    const save = screen.getByRole("button", { name: /save/i });
    expect(save).toBeEnabled();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "   " } });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "" } });
    expect(save).toBeDisabled();
  });

  it("saves the trimmed name plus system and language, then calls onSaved", async () => {
    const updated: { req?: Record<string, unknown> } = {};
    const onSaved = vi.fn();
    renderForm(
      { campaign: makeCampaign({ id: "camp-9", system: "D&D 5e", language: "en" }), onSaved },
      mockBackend({ languages: ["de", "en"], updated }),
    );

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "  Renamed Quest  " } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(updated.req).toEqual({
      id: "camp-9",
      name: "Renamed Quest",
      system: "D&D 5e",
      language: "en",
      tapeArmed: false,
    });
  });

  it("calls onCancel when cancel is clicked", () => {
    const onCancel = vi.fn();
    renderForm({ onCancel });
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  // Highlight recording (the rollover tape, #412, ADR-0051): the Campaign-level
  // GM opt-in that arms consent-gated capture. Default OFF, round-trips through
  // UpdateCampaign like the other fields — but its switch sits inside the
  // closed-by-default AdvancedCard, so every test opens the disclosure first.
  it("prefills the Highlight recording toggle from the campaign prop", () => {
    renderForm({ campaign: makeCampaign({ tapeArmed: true }) });
    openAdvanced();
    expect(screen.getByRole("switch", { name: /highlight recording/i })).toBeChecked();
  });

  it("explains the consent gating and next-session effectiveness in plain language", () => {
    renderForm();
    openAdvanced();
    // The simplified hint keeps the three facts a GM must know: a consent
    // message is posted in the voice channel, only consenting speakers are
    // recorded, and the change applies from the next session.
    expect(screen.getByText(/consent message.*voice channel/i)).toBeInTheDocument();
    expect(screen.getByText(/only speakers who consent are recorded/i)).toBeInTheDocument();
    expect(screen.getByText(/applies from the next session/i)).toBeInTheDocument();
  });

  it("keeps the Highlight recording switch out of the tree while the advanced card is closed", () => {
    // The AdvancedCard unmounts its body when closed — collapsed settings are
    // out of the accessibility tree, not merely hidden.
    renderForm();
    expect(screen.queryByRole("switch", { name: /highlight recording/i })).not.toBeInTheDocument();
  });

  it("round-trips the tape-armed field through UpdateCampaign when toggled on", async () => {
    const updated: { req?: Record<string, unknown> } = {};
    const onSaved = vi.fn();
    renderForm(
      { campaign: makeCampaign({ id: "camp-9", system: "D&D 5e", language: "en", tapeArmed: false }), onSaved },
      mockBackend({ languages: ["de", "en"], updated }),
    );

    openAdvanced();
    fireEvent.click(screen.getByRole("switch", { name: /highlight recording/i }));
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(updated.req).toEqual({
      id: "camp-9",
      name: "The Sunless Citadel",
      system: "D&D 5e",
      language: "en",
      tapeArmed: true,
    });
  });
});
