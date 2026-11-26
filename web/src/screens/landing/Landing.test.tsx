import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { render, screen, waitFor, fireEvent, within } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  AdmissionMode,
  GetAdmissionModeResponseSchema,
  GetCurrentUserResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { I18nProvider, en } from "@/i18n";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";
import { Landing } from "./Landing";

// The public landing surface (#521). What is load-bearing:
//   1. the ROOT serves it to a visitor with no session (it used to bounce them
//      into a bare login form), while a signed-in visitor still goes straight to
//      the app;
//   2. both calls to action — the hero's and the closing one — go to /login:
//      the OAuth flow, and #518's open-mode acknowledgment in front of it, are
//      untouched;
//   3. the free-beta economics only render on an explicitly OPEN deployment —
//      the SPA ships identically to every operator, and a stock allowlist
//      install must not advertise a signup it rejects or an allowance it does
//      not fund. Fail-safe (probe down) is the neutral page;
//   4. the page shows the product (a real capture beside the headline, a
//      screen for every story row, each one enlargeable) and says how to get
//      started in the app's own words.

// jsdom has no modal <dialog>: give it the two methods the lightbox calls, with
// the two effects that matter here (the `open` attribute, the close event).
beforeAll(() => {
  const proto = HTMLDialogElement.prototype as HTMLDialogElement & { __stubbed?: boolean };
  if (proto.__stubbed) return;
  proto.__stubbed = true;
  proto.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  proto.close = function close(this: HTMLDialogElement) {
    if (!this.hasAttribute("open")) return;
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
});

afterEach(() => {
  localStorage.clear();
});

function backend(opts: { signedIn?: boolean; mode?: AdmissionMode; down?: boolean } = {}) {
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => {
        if (opts.down) throw new ConnectError("origin wedged", Code.Unavailable);
        if (!opts.signedIn) throw new ConnectError("no session", Code.Unauthenticated);
        return create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        });
      },
      getAdmissionMode: () => {
        if (opts.down) throw new ConnectError("origin wedged", Code.Unavailable);
        return create(GetAdmissionModeResponseSchema, {
          admissionMode: opts.mode ?? AdmissionMode.OPEN,
        });
      },
    });
  });
}

function renderRoot(opts: { signedIn?: boolean; mode?: AdmissionMode; down?: boolean } = {}) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  render(
    <Providers transport={backend(opts)} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

describe("root landing surface", () => {
  it("serves the landing page to a visitor with no session", async () => {
    renderRoot();
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(/NPCs a voice/i);
  });

  it("sends both calls to action — the hero's and the closing one — into the unchanged auth flow", async () => {
    renderRoot();
    await screen.findByRole("heading", { level: 1 });
    // /login, not /auth/discord/login: the login screen is where the open-mode
    // AUP acknowledgment (#518) gates the OAuth start.
    const ctas = await screen.findAllByRole("link", { name: "Start with Discord" });
    expect(ctas).toHaveLength(2);
    for (const cta of ctas) expect(cta).toHaveAttribute("href", "/login");
    // …and the bar's Sign in does too.
    const bar = screen.getByRole("banner");
    expect(within(bar).getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });

  it("takes a signed-in visitor straight to the app", async () => {
    const router = renderRoot({ signedIn: true });
    await waitFor(() =>
      // The tenant index decides where the app opens: the campaign's Overview
      // (ADR-0063 amendment), no longer Setup.
      expect(router.state.location.pathname).toBe("/t/default/campaign/overview"),
    );
    expect(screen.queryByText(/NPCs a voice/i)).not.toBeInTheDocument();
  });

  it("serves the landing page when the backend is down, not a spinner", async () => {
    // The stated design goal: the marketing surface is a static document and
    // stays up when the API is not (a rejected probe — a HUNG one is bounded
    // by RootEntry's grace timer, not exercised here).
    renderRoot({ down: true });
    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(/NPCs a voice/i);
    // Fail-safe posture: no free-beta economics without an explicit OPEN answer.
    expect(screen.queryByText(/free while in beta/i)).not.toBeInTheDocument();
  });

  it("only advertises the free beta on an explicitly OPEN deployment", async () => {
    renderRoot({ mode: AdmissionMode.ALLOWLIST });
    await screen.findByRole("heading", { level: 1 });
    // A stock allowlist install: neutral sign-in CTA (twice), no signup
    // framing, no allowance promise this instance does not fund.
    const ctas = screen.getAllByRole("link", { name: "Sign in with Discord" });
    expect(ctas).toHaveLength(2);
    for (const cta of ctas) expect(cta).toHaveAttribute("href", "/login");
    expect(screen.queryByText(/free while in beta/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/small monthly allowance/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Free beta" })).not.toBeInTheDocument();
    // The transcription honesty note is deployment-independent and stays.
    expect(screen.getByText(/sessions are transcribed/i)).toBeInTheDocument();
  });

  it("names the browser tab for the page, and hands it back when the visitor leaves", async () => {
    const router = renderRoot();
    await screen.findByRole("heading", { level: 1 });
    expect(document.title).toBe("Voiced NPCs for tabletop RPGs on Discord · Glyphoxa");
    await router.navigate({ to: "/login" });
    await waitFor(() => expect(document.title).toBe("Sign in · Glyphoxa"));
  });
});

describe("landing copy", () => {
  it("states the beta bargain on an open deployment: a small allowance, then your own keys", async () => {
    render(<Landing open />);
    const doc = document.body.textContent ?? "";
    expect(doc).toMatch(/small monthly allowance/i);
    expect(doc).toMatch(/your own AI service keys/i);
    // Honest about what the beta is (#258's copy policy): no card, rough edges,
    // and the transcription disclosure players will meet in Discord.
    expect(doc).toMatch(/no card/i);
    expect(doc).toMatch(/transcribed/i);
    // The allowance gate is plan-based: adding keys alone does not unpause an
    // exhausted trial, the operator moves the tenant to the BYOK plan. The copy
    // must not promise the self-service switch that does not exist.
    expect(doc).not.toMatch(/until you add your own keys/i);
    expect(doc).toMatch(/ask the operator/i);
  });

  it("presents the beta as two plan cards with their terms as bullets, and links the person to ask", async () => {
    render(<Landing open />);
    const beta = screen.getByRole("region", { name: "How the free beta works" });
    const plans = within(beta).getAllByRole("article");
    expect(plans.map((p) => within(p).getByRole("heading", { level: 3 }).textContent)).toEqual([
      "Try it on us",
      "Then bring your own keys",
    ]);
    for (const plan of plans) {
      expect(within(plan).getAllByRole("listitem")).toHaveLength(3);
      // "Ask the operator" leads to the operator's contact: the Impressum.
      expect(within(plan).getByRole("link", { name: "ask the operator" })).toHaveAttribute("href", "/imprint");
    }
  });

  it("quotes no allowance figure the operator can change out from under it", async () => {
    // included_usage_usd is an operator-adjustable chart value (#521's plan
    // catalog), so a dollar figure in this copy would become false on the first
    // `helm upgrade` that tunes it.
    for (const open of [true, false]) {
      const { unmount } = render(<Landing open={open} />);
      expect(document.body.textContent ?? "").not.toMatch(/[$€]\s?\d|\d\s?[$€]/);
      unmount();
    }
  });

  it("does not call the hosted beta 'self-hosted', and does call a self-run install that", async () => {
    const hosted = render(<Landing open />);
    expect(document.body.textContent ?? "").not.toMatch(/self-hosted/i);
    hosted.unmount();

    render(<Landing />);
    expect(screen.getByText(/Glyphoxa is a self-hosted companion/)).toBeInTheDocument();
  });

  it("carries the legal footer for an unauthenticated visitor", async () => {
    render(<Landing />);
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
  });

  it("is localized, the language picker included", async () => {
    localStorage.setItem("gx-lang", "de");
    render(
      <I18nProvider>
        <Landing open />
      </I18nProvider>,
    );
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Gib deinen NPCs eine Stimme am Spieltisch");
    expect(screen.getAllByRole("link", { name: "Mit Discord loslegen" })).toHaveLength(2);
    expect(screen.getByRole("heading", { name: "So funktioniert es" })).toBeInTheDocument();
    // A visitor can switch before any session exists.
    expect(screen.getByRole("combobox", { name: "Anzeigesprache" })).toBeInTheDocument();
  });
});

describe("landing structure", () => {
  it("shows the product on the first screen: the live-session capture beside the headline", () => {
    const { container } = render(<Landing />);
    const hero = container.querySelector(".gx-landing__hero") as HTMLElement;
    expect(within(hero).getByRole("heading", { level: 1 })).toBeInTheDocument();
    const shot = within(hero).getByRole("img");
    expect(shot).toHaveAttribute("src", "/shots/session-live.webp");
    // Above the fold: fetched at once, with its box reserved.
    expect(shot).toHaveAttribute("loading", "eager");
    expect(shot).toHaveAttribute("width", "1600");
    expect(shot).toHaveAttribute("height", "1000");
    expect(shot).toHaveAccessibleName(/live session screen/i);
  });

  it("tells the story in five rows, each with its screen and a caption", () => {
    render(<Landing />);
    const story = screen.getByRole("region", { name: "What Glyphoxa does" });
    const rows = within(story).getAllByRole("article");
    expect(rows.map((row) => row.id)).toEqual([
      "at-the-table",
      "after-the-session",
      "your-world",
      "prep",
      "your-keys",
    ]);
    const shots: string[] = [];
    for (const row of rows) {
      expect(within(row).getByRole("heading", { level: 3 })).not.toBeEmptyDOMElement();
      const figures = row.querySelectorAll("figure");
      expect(figures.length).toBeGreaterThanOrEqual(1);
      for (const figure of figures) {
        const img = figure.querySelector("img") as HTMLImageElement;
        shots.push(img.getAttribute("src") ?? "");
        // Below the fold: not fetched until the visitor scrolls near.
        expect(img).toHaveAttribute("loading", "lazy");
        const caption = figure.querySelector("figcaption")?.textContent ?? "";
        expect(caption).not.toBe("");
        expect(img).toHaveAttribute("alt", caption);
      }
    }
    // Every real capture is used exactly once across the hero and the story.
    expect(shots.sort()).toEqual(
      [
        "/shots/cast-persona.webp",
        "/shots/maps.webp",
        "/shots/planning.webp",
        "/shots/session-highlights.webp",
        "/shots/session-search.webp",
        "/shots/setup-keys.webp",
        "/shots/wiki-graph.webp",
      ].sort(),
    );
  });

  it("offers section links in the bar that lead to sections that exist", () => {
    const { container } = render(<Landing open />);
    const nav = screen.getByRole("navigation", { name: "On this page" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["What it does", "#story"],
      ["How it works", "#how"],
      ["Free beta", "#beta"],
      ["Privacy", "#privacy"],
    ]);
    for (const link of links) {
      const id = (link.getAttribute("href") ?? "").slice(1);
      expect(container.querySelector(`#${id}`), `#${id}`).toBeInTheDocument();
    }
    // The secondary hero link and the brand lead somewhere real too.
    expect(screen.getByRole("link", { name: /see how it works/i })).toHaveAttribute("href", "#how");
    expect(container.querySelector("#top")).toBeInTheDocument();
  });

  it("opens a shared link to a section at that section", () => {
    // The landing renders only once the session probe has answered, after
    // the browser looked for #how: the page looks again itself.
    const scrolled: Element[] = [];
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
      scrolled.push(this);
    };
    window.history.replaceState(null, "", "/#how");
    try {
      const { container } = render(<Landing />);
      expect(scrolled).toEqual([container.querySelector("#how")]);
    } finally {
      Element.prototype.scrollIntoView = original;
      window.history.replaceState(null, "", "/");
    }
  });

  it("ends with a call to action, then the footer", () => {
    const { container } = render(<Landing />);
    const sections = [...container.querySelectorAll("main > section")];
    const last = sections[sections.length - 1] as HTMLElement;
    expect(within(last).getByRole("heading", { level: 2 })).toHaveTextContent("Your NPCs are waiting for a voice");
    expect(within(last).getByRole("link", { name: "Sign in with Discord" })).toHaveAttribute("href", "/login");

    const footer = screen.getByRole("contentinfo");
    expect(within(footer).getByRole("link", { name: "Source code on GitHub" })).toHaveAttribute(
      "href",
      "https://github.com/MrWong99/Glyphoxa",
    );
    expect(within(footer).getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
  });

  it("puts the transcription note in a 'Privacy at the table' callout that links the Datenschutzerklärung", () => {
    render(<Landing />);
    const privacy = screen.getByRole("region", { name: "Privacy at the table" });
    expect(privacy).toHaveTextContent(/sessions are transcribed/i);
    expect(privacy).toHaveTextContent(/opt-in per player/i);
    expect(within(privacy).getByRole("link", { name: "Datenschutzerklärung" })).toHaveAttribute("href", "/privacy");
  });
});

describe("landing 'How it works'", () => {
  it("is a numbered list of five steps", () => {
    render(<Landing />);
    const how = screen.getByRole("region", { name: "How it works" });
    const steps = within(within(how).getByRole("list")).getAllByRole("listitem");
    expect(steps).toHaveLength(5);
    expect(steps.map((s) => within(s).getByRole("heading", { level: 3 }).textContent)).toEqual([
      "Sign in with Discord",
      "Connect your Discord server",
      "Add your AI service keys",
      "Create a campaign and its cast",
      "Start a session",
    ]);
  });

  // The steps name screens, sections and buttons. They are quoted from the
  // app's own catalog here, so a rename in the app that leaves this page
  // describing a screen that no longer exists fails this test.
  it("uses the words the app itself uses for the screens, sections and buttons it sends the GM to", () => {
    const step = (n: 2 | 3 | 4 | 5) => en[`auth.howStep${n}Body`];
    expect(step(2)).toContain(en["shell.navSetup"]);
    expect(step(2)).toContain(`“${en["config.discordTitle"]}”`);
    // "add Glyphoxa to your server" — the Setup button, in a sentence.
    expect(step(2).toLowerCase()).toContain(en["config.addBotAction"].toLowerCase());

    expect(step(3)).toContain(en["shell.navSetup"]);
    expect(step(3)).toContain(`“${en["config.aiServiceKeysTitle"]}”`);
    expect(step(3)).toContain(en["config.kindAiModel"]);
    expect(step(3).toLowerCase()).toContain(en["config.kindVoiceSpeech"].toLowerCase());
    expect(step(3)).toContain(`“${en["config.badgeWorking"]}”`);

    expect(step(4)).toContain(en["campaign.tabCast"]);
    expect(step(4)).toContain("Butler");

    expect(step(5)).toContain(en["shell.navSession"]);
    expect(step(5).toLowerCase()).toContain(en["session.voiceChannel"].toLowerCase());
    expect(step(5)).toContain(`“${en["session.startSession"]}”`);
  });

  it("says what is different on the hosted beta: signing in founds a table, and keys can wait", () => {
    const { unmount } = render(<Landing open />);
    let how = screen.getByRole("region", { name: "How it works" });
    expect(how).toHaveTextContent(/your first sign-in creates your own table/i);
    expect(how).toHaveTextContent(/on the free beta you can skip this at first/i);
    unmount();

    render(<Landing />);
    how = screen.getByRole("region", { name: "How it works" });
    expect(how).not.toHaveTextContent(/creates your own table/i);
    expect(how).not.toHaveTextContent(/free beta/i);
  });
});

describe("landing lightbox", () => {
  const dialog = () => document.querySelector("dialog.gx-lightbox") as HTMLDialogElement;

  it("opens a story image large when it is clicked, and closes on Escape", () => {
    render(<Landing />);
    expect(dialog()).not.toHaveAttribute("open");

    const tile = screen.getByRole("button", { name: /view larger: the world wiki as a relationship map/i });
    fireEvent.click(tile);

    expect(dialog()).toHaveAttribute("open");
    // The dialog is named by the capture it shows, and shows that capture.
    expect(dialog()).toHaveAccessibleName(/world wiki as a relationship map/i);
    const large = within(dialog()).getByRole("img");
    expect(large).toHaveAttribute("src", "/shots/wiki-graph.webp");
    expect(within(dialog()).getByText(/who knows whom/i)).toBeInTheDocument();

    fireEvent.keyDown(dialog(), { key: "Escape" });
    expect(dialog()).not.toHaveAttribute("open");
    expect(within(dialog()).queryByRole("img")).not.toBeInTheDocument();
  });

  it("opens the hero capture too, and closes from its Close button", () => {
    render(<Landing />);
    fireEvent.click(screen.getByRole("button", { name: /view larger: the live session screen/i }));
    expect(within(dialog()).getByRole("img")).toHaveAttribute("src", "/shots/session-live.webp");

    fireEvent.click(within(dialog()).getByRole("button", { name: "Close" }));
    expect(dialog()).not.toHaveAttribute("open");
  });

  it("closes on a click on the backdrop, but not on a click on the picture", () => {
    render(<Landing />);
    fireEvent.click(screen.getByRole("button", { name: /view larger: planning chat/i }));

    fireEvent.click(within(dialog()).getByRole("img"));
    expect(dialog()).toHaveAttribute("open");

    // The backdrop is the dialog element itself.
    fireEvent.click(dialog());
    expect(dialog()).not.toHaveAttribute("open");
  });

  it("can be reopened on another image", () => {
    render(<Landing />);
    fireEvent.click(screen.getByRole("button", { name: /view larger: planning chat/i }));
    fireEvent.keyDown(dialog(), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: /view larger: maps/i }));
    expect(dialog()).toHaveAttribute("open");
    expect(within(dialog()).getByRole("img")).toHaveAttribute("src", "/shots/maps.webp");
  });

  it("makes every capture on the page enlargeable — eight tiles, eight buttons", () => {
    render(<Landing />);
    const tiles = screen.getAllByRole("button", { name: /^view larger: /i });
    expect(tiles).toHaveLength(8);
    for (const tile of tiles) expect(tile).toHaveAttribute("aria-haspopup", "dialog");
  });
});
