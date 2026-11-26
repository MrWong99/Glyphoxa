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

type Camp = {
  id?: string;
  name?: string;
  system?: string;
  language?: string;
  tapeArmed?: boolean;
  highlightBar?: number;
  highlightConfirmWindows?: number;
};

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
            highlightBar: req.highlightBar,
            highlightConfirmWindows: req.highlightConfirmWindows,
          };
        return create(UpdateCampaignResponseSchema, {
          campaign: create(CampaignSchema, {
            id: req.id,
            name: req.name,
            system: req.system,
            language: req.language,
            tapeArmed: req.tapeArmed ?? false,
            highlightBar: req.highlightBar ?? 0,
            highlightConfirmWindows: req.highlightConfirmWindows ?? 0,
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
    highlightBar: c.highlightBar ?? 0,
    highlightConfirmWindows: c.highlightConfirmWindows ?? 0,
  });
}

function renderForm(
  props: {
    campaign?: ReturnType<typeof makeCampaign>;
    onSaved?: () => void;
    onCancel?: () => void;
    onDirtyChange?: (dirty: boolean) => void;
    variant?: "page" | "dialog";
  } = {},
  transport = mockBackend(),
) {
  const onSaved = props.onSaved ?? vi.fn();
  const onCancel = props.onCancel ?? vi.fn();
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CampaignSettingsForm
        campaign={props.campaign ?? makeCampaign()}
        onSaved={onSaved}
        onCancel={onCancel}
        onDirtyChange={props.onDirtyChange}
        variant={props.variant}
      />
    </Providers>,
  );
  return { onSaved, onCancel };
}

// openLanguageSelect opens the Radix language Select and returns the listbox.
async function openLanguageSelect() {
  fireEvent.keyDown(screen.getByRole("combobox", { name: /language/i }), { key: "Enter" });
  return screen.getByRole("listbox");
}

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
      // The knob fields always ride explicitly; 0 = engine default (#632 follow-up).
      highlightBar: 0,
      highlightConfirmWindows: 0,
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
  // UpdateCampaign like the other fields. It sits in a section of its own,
  // shown openly — it used to be behind an "Advanced settings" disclosure.
  it("shows the Session Highlights settings openly, in a section of their own", () => {
    renderForm();
    const section = screen.getByRole("region", { name: "Session Highlights" });
    expect(within(section).getByRole("switch", { name: /highlight recording/i })).toBeInTheDocument();
    expect(within(section).getByLabelText(/highlight score bar/i)).toBeInTheDocument();
    expect(within(section).getByLabelText(/confirmation windows/i)).toBeInTheDocument();
    // Name, System and Language are the other section.
    expect(within(screen.getByRole("region", { name: "General" })).getByLabelText("Name")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /advanced settings/i })).not.toBeInTheDocument();
  });

  it("prefills the Highlight recording toggle from the campaign prop", () => {
    renderForm({ campaign: makeCampaign({ tapeArmed: true }) });
    expect(screen.getByRole("switch", { name: /highlight recording/i })).toBeChecked();
  });

  it("explains the consent gating and next-session effectiveness in plain language", () => {
    renderForm();
    // The simplified hint keeps the three facts a GM must know: a consent
    // message is posted in the voice channel, only consenting speakers are
    // recorded, and the change applies from the next session. (The next-session
    // clause is asserted on the consent hint node — the tuning hint below the
    // knobs carries the same phrase.)
    const hint = screen.getByText(/consent message.*voice channel/i);
    expect(hint).toHaveTextContent(/only speakers who consent are recorded/i);
    expect(hint).toHaveTextContent(/applies from the next session/i);
  });

  it("round-trips the tape-armed field through UpdateCampaign when toggled on", async () => {
    const updated: { req?: Record<string, unknown> } = {};
    const onSaved = vi.fn();
    renderForm(
      { campaign: makeCampaign({ id: "camp-9", system: "D&D 5e", language: "en", tapeArmed: false }), onSaved },
      mockBackend({ languages: ["de", "en"], updated }),
    );

    fireEvent.click(screen.getByRole("switch", { name: /highlight recording/i }));
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(updated.req).toEqual({
      id: "camp-9",
      name: "The Sunless Citadel",
      system: "D&D 5e",
      language: "en",
      tapeArmed: true,
      highlightBar: 0,
      highlightConfirmWindows: 0,
    });
  });

  // Session-Highlights tuning (#632 follow-up): the bar/confirm-windows pair
  // sits in the same section as the arming switch (the knobs only act while
  // recording is armed). 0 on the wire = engine default, shown as an empty
  // field with the default in the placeholder.
  it("shows empty knob fields with default placeholders for an untuned campaign", () => {
    renderForm({ campaign: makeCampaign() });
    const bar = screen.getByLabelText(/highlight score bar/i) as HTMLInputElement;
    const confirm = screen.getByLabelText(/confirmation windows/i) as HTMLInputElement;
    expect(bar.value).toBe("");
    expect(bar.placeholder).toMatch(/8 \(default\)/);
    expect(confirm.value).toBe("");
    expect(confirm.placeholder).toMatch(/2 \(default\)/);
  });

  it("prefills the knob fields from a tuned campaign", () => {
    renderForm({ campaign: makeCampaign({ highlightBar: 4.5, highlightConfirmWindows: 1 }) });
    expect((screen.getByLabelText(/highlight score bar/i) as HTMLInputElement).value).toBe("4.5");
    expect((screen.getByLabelText(/confirmation windows/i) as HTMLInputElement).value).toBe("1");
  });

  it("round-trips the knob values through UpdateCampaign, and clearing sends 0", async () => {
    const updated: { req?: Record<string, unknown> } = {};
    const onSaved = vi.fn();
    renderForm(
      {
        campaign: makeCampaign({ id: "camp-9", highlightBar: 7, highlightConfirmWindows: 3 }),
        onSaved,
      },
      mockBackend({ languages: ["de", "en"], updated }),
    );

    fireEvent.change(screen.getByLabelText(/highlight score bar/i), { target: { value: "4.5" } });
    // Clearing the confirm field is the reset gesture: it must ride as 0.
    fireEvent.change(screen.getByLabelText(/confirmation windows/i), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(updated.req).toEqual({
      id: "camp-9",
      name: "The Sunless Citadel",
      system: "D&D 5e",
      language: "en",
      tapeArmed: false,
      highlightBar: 4.5,
      highlightConfirmWindows: 0,
    });
  });
});

describe("CampaignSettingsForm: saving is explicit and visible", () => {
  it("says when something is unsaved, reports it to the host, and says Saved afterwards", async () => {
    const onDirtyChange = vi.fn();
    renderForm({ onDirtyChange });
    // Nothing to report on a form nobody touched.
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed Quest" } });
    expect(screen.getByRole("status")).toHaveTextContent("Unsaved changes");
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(true));

    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved"));
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false));
  });

  it("is clean again when an edit is typed back to what is saved", () => {
    renderForm();
    const name = screen.getByLabelText("Name");
    fireEvent.change(name, { target: { value: "Something else" } });
    expect(screen.getByRole("status")).toHaveTextContent("Unsaved changes");
    fireEvent.change(name, { target: { value: "The Sunless Citadel" } });
    expect(screen.getByRole("status")).toHaveTextContent("");
  });

  it("keeps the edits, and says why, when the save is refused", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(CampaignService, {
        listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
        updateCampaign: () => {
          throw new ConnectError("highlight_bar must be between 0 and 10", Code.InvalidArgument);
        },
      });
    });
    const { onSaved } = renderForm({}, transport);
    fireEvent.change(screen.getByLabelText(/highlight score bar/i), { target: { value: "11" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Couldn't save: highlight_bar must be between 0 and 10",
    );
    expect(onSaved).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/highlight score bar/i)).toHaveValue(11);
  });

  it("keeps Save focusable while a save is in flight, and sends nothing twice", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let updates = 0;
    const transport = createRouterTransport(({ service }) => {
      service(CampaignService, {
        listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
        updateCampaign: async (req) => {
          updates++;
          await gate;
          return create(UpdateCampaignResponseSchema, { campaign: create(CampaignSchema, { id: req.id, name: req.name }) });
        },
      });
    });
    const { onSaved } = renderForm({}, transport);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Renamed Quest" } });
    const save = screen.getByRole("button", { name: "Save changes" });
    save.focus();
    fireEvent.click(save);

    // Busy, named for what it is doing — and NOT disabled: a button that turns
    // disabled under the keyboard drops focus to <body>.
    const saving = await screen.findByRole("button", { name: "Saving…" });
    expect(saving).toBe(save);
    expect(saving).toHaveAttribute("aria-busy", "true");
    expect(saving).toBeEnabled();
    expect(saving).toHaveFocus();

    // Enter again while it is in flight: nothing more is sent.
    fireEvent.click(saving);
    fireEvent.submit(saving.closest("form")!);
    release();
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(updates).toBe(1);
    expect(screen.getByRole("button", { name: "Save changes" })).toHaveFocus();
  });

  it("names a missing name instead of only greying Save out", () => {
    renderForm();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "" } });
    expect(screen.getByText("A campaign needs a name.")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
  });
});

describe("CampaignSettingsForm: the two hosts", () => {
  it("on the page, offers Discard changes instead of Cancel, and it restores what is saved", async () => {
    const onCancel = vi.fn();
    renderForm({ variant: "page", onCancel, campaign: makeCampaign({ tapeArmed: true, highlightBar: 6 }) });
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    const discard = screen.getByRole("button", { name: "Discard changes" });
    // Nothing to discard yet.
    expect(discard).toBeDisabled();

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Half an idea" } });
    fireEvent.click(screen.getByRole("switch", { name: /highlight recording/i }));
    fireEvent.change(screen.getByLabelText(/highlight score bar/i), { target: { value: "" } });
    expect(discard).toBeEnabled();

    fireEvent.click(discard);
    expect(screen.getByLabelText("Name")).toHaveValue("The Sunless Citadel");
    expect(screen.getByRole("switch", { name: /highlight recording/i })).toBeChecked();
    expect(screen.getByLabelText(/highlight score bar/i)).toHaveValue(6);
    expect(screen.getByRole("status")).toHaveTextContent("");
    // The button it was pressed on is disabled again; focus went to the first
    // field, not to <body>.
    expect(screen.getByLabelText("Name")).toHaveFocus();
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("heads its sections one level under its host's title", () => {
    renderForm({ variant: "page" });
    expect(screen.getByRole("heading", { level: 2, name: "General" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Session Highlights" })).toBeInTheDocument();
  });

  it("gives two forms on one page ids of their own", () => {
    // The Settings view and a Settings dialog (or, before, the topbar popover)
    // can be mounted together; a shared id makes a label name the other form's
    // control.
    render(
      <Providers transport={mockBackend()} queryClient={makeQueryClient()}>
        <CampaignSettingsForm campaign={makeCampaign()} onSaved={() => {}} variant="page" />
        <CampaignSettingsForm campaign={makeCampaign({ id: "camp-2" })} onSaved={() => {}} />
      </Providers>,
    );
    const ids = Array.from(document.querySelectorAll("[id]")).map((el) => el.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(screen.getAllByRole("switch", { name: /highlight recording/i })).toHaveLength(2);
  });
});
