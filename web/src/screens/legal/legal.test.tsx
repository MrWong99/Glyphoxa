import { describe, it, expect, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";

import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";
import { OPERATOR } from "./operator";

// The legal documents (#518). Two properties matter beyond "the text renders":
//
//   1. They are reachable WITHOUT a session — a visitor deciding whether to sign
//      up, and a player at a transcribed table who will never open the app, both
//      have to be able to read them. The transport below fails every RPC, so a
//      page that needed the backend would fail these tests.
//   2. /privacy is a contract with the deployment: the Helm chart derives
//      GLYPHOXA_PRIVACY_POLICY_URL as <host>/privacy for the Bot's in-channel
//      transcription disclosure (#519). A renamed route breaks a link players
//      see in Discord, so the path is pinned here.

// deadTransport answers every RPC with a rejected promise: proof that the legal
// routes render with no working backend and no session.
function deadTransport() {
  return createRouterTransport(() => {});
}

function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <Providers transport={deadTransport()} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

afterEach(() => {
  localStorage.clear();
});

describe("legal routes", () => {
  it("serves the Impressum unauthenticated at /imprint", async () => {
    renderAt("/imprint");
    expect(await screen.findByRole("heading", { name: "Impressum", level: 1 })).toBeInTheDocument();
    // §5 DDG: the operator, an address, and a contact that reaches a human.
    expect(screen.getByRole("heading", { name: /Diensteanbieter/i })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Kontakt/i })).toBeInTheDocument();
  });

  it("serves the Datenschutzerklärung unauthenticated at /privacy", async () => {
    renderAt("/privacy");
    expect(
      await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 }),
    ).toBeInTheDocument();
  });

  it("serves the Nutzungsbedingungen unauthenticated at /terms", async () => {
    renderAt("/terms");
    expect(
      await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 }),
    ).toBeInTheDocument();
  });

  it("answers the German aliases people actually type", async () => {
    renderAt("/impressum");
    expect(await screen.findByRole("heading", { name: "Impressum", level: 1 })).toBeInTheDocument();
  });

  it("covers every data flow the deployment has to disclose", async () => {
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const doc = document.body.textContent ?? "";

    // The disclosures decided on #518: Discord OAuth, always-on transcription
    // (lines, chunks, embeddings), the opt-in tape, the provider subprocessors,
    // residency, retention, and the GM-as-controller framing.
    for (const topic of [
      "Discord",
      "transkribiert",
      "Einbettungen",
      "Session Highlights",
      "ElevenLabs",
      "Groq",
      "Anthropic",
      "Gemini",
      OPERATOR.edgeProvider,
      "Auftragsverarbeitung",
      "Speicherdauer",
      "Aufsichtsbehörde",
    ]) {
      expect(doc, `Datenschutzerklärung must mention ${topic}`).toContain(topic);
    }
  });

  it("names the declared edge provider as a recipient, and what it can see", async () => {
    // This deployment is reached through a Cloudflare Tunnel, so the proxy that
    // terminates its TLS is a recipient that must be named (Art. 13 Abs. 1
    // lit. e DSGVO) — including the part operators forget: TLS ends there, so
    // it sees the web traffic in the clear. The empty case (a deployment
    // reached directly) is covered in edgeProvider.test.tsx.
    expect(OPERATOR.edgeProvider).not.toBe("");
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const doc = document.body.textContent ?? "";
    expect(doc).toContain(OPERATOR.edgeProvider);
    expect(doc).toContain("TLS-Terminierung");
    // The audio split, stated rather than implied — and it IS a split, which an
    // earlier draft got wrong. LIVE session audio never crosses the proxy: the
    // voice tier dials Discord and the speech providers outbound. But stored
    // Highlight clips are served BY the web tier
    // (HighlightsStrip renders <audio src="/api/v1/highlights/{id}/clip">), so
    // they cross it like any other web content. Both halves are material to the
    // players whose consented voice is in those clips.
    expect(doc).toMatch(/Live-Audio einer Sprachsitzung läuft nicht über diesen Weg/);
    expect(doc).toMatch(/Highlight-Clips .* über diesen Anbieter ausgeliefert/);
  });

  it("links the other documents from every legal page", async () => {
    renderAt("/terms");
    await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 });
    const footer = screen.getByRole("navigation", { name: "Legal" });
    expect(within(footer).getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(within(footer).getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
  });

  it("renders the filled operator identity without the TODO banner", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    // This repository ships the identity of the canonical beta deployment,
    // filled in by its operator on 2026-07-26 — so no banner here. The banner
    // machinery itself stays covered in todoBanner.test.tsx against a mocked,
    // unfilled identity, and operatorFilled.test.ts (npm run check:operator)
    // holds the fork-safe completeness check an operator runs before deploying.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText(/Lukas Schmidt/)).toBeInTheDocument();
  });
});

describe("legal page chrome", () => {
  it("leads back to the start page from the brand", async () => {
    renderAt("/privacy");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    // The start page (the landing), not /login: a visitor reading the
    // Datenschutzerklärung before deciding has not signed in to anything.
    expect(screen.getByRole("link", { name: "Glyphoxa — start page" })).toHaveAttribute("href", "/");
  });

  it("cross-links the three documents at the top, marking the one that is open", async () => {
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const docs = screen.getByRole("navigation", { name: "Legal documents" });
    const links = within(docs).getAllByRole("link");
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Impressum", "/imprint"],
      ["Datenschutzerklärung", "/privacy"],
      ["Nutzungsbedingungen", "/terms"],
    ]);
    // The alias still marks the document it serves.
    expect(within(docs).getByRole("link", { name: "Datenschutzerklärung" })).toHaveAttribute("aria-current", "page");
    expect(within(docs).getByRole("link", { name: "Impressum" })).not.toHaveAttribute("aria-current");
  });

  it("lists the document's sections under 'Inhalt', each link leading to its section", async () => {
    renderAt("/terms");
    await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 });
    const toc = screen.getByRole("navigation", { name: "Inhalt" });
    const links = within(toc).getAllByRole("link");
    const sections = screen.getAllByRole("heading", { level: 2 });
    // One entry per section, in order, named like the section.
    expect(links.map((a) => a.textContent)).toEqual(sections.map((h) => h.textContent));
    for (const link of links) {
      const id = (link.getAttribute("href") ?? "").replace(/^#/, "");
      const target = document.getElementById(id);
      expect(target, `#${id}`).not.toBeNull();
      expect(within(target as HTMLElement).getByRole("heading", { level: 2 })).toHaveTextContent(
        link.textContent ?? "",
      );
    }
  });

  it("gives the sections stable, readable ids", async () => {
    // A link into a document (#speicherdauer, sent to a player who asked)
    // names its section by the heading's words, without its number, so
    // renumbering a section does not change the id. (That such a link also
    // OPENS at its section is the next test.)
    renderAt("/privacy");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    expect(document.getElementById("speicherdauer")).toHaveTextContent(/^7\. Speicherdauer/);
    expect(document.getElementById("aenderungen-dieser-erklaerung")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "7. Speicherdauer" })).toHaveAttribute("href", "#speicherdauer");
  });

  it("opens a shared link to a section at that section", async () => {
    // The document is a lazily loaded chunk: the browser (and the router)
    // looked for #speicherdauer before it existed, so the page itself has to
    // look again once its sections are rendered. The router is handed the
    // path WITHOUT the hash so its own attempt cannot stand in for the page's.
    const scrolled: Element[] = [];
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
      scrolled.push(this);
    };
    window.history.replaceState(null, "", "/privacy#speicherdauer");
    try {
      renderAt("/privacy");
      await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
      await waitFor(() => expect(scrolled).toContain(document.getElementById("speicherdauer")));
    } finally {
      Element.prototype.scrollIntoView = original;
      window.history.replaceState(null, "", "/");
    }
  });

  it("stays at the top when the link names no section", async () => {
    const scrolled: Element[] = [];
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
      scrolled.push(this);
    };
    window.history.replaceState(null, "", "/privacy#no-such-section");
    try {
      renderAt("/privacy");
      await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
      expect(scrolled).toEqual([]);
    } finally {
      Element.prototype.scrollIntoView = original;
      window.history.replaceState(null, "", "/");
    }
  });

  it("finds sections the operator's identity decides on, too", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    const toc = screen.getByRole("navigation", { name: "Inhalt" });
    expect(within(toc).getByRole("link", { name: "Streitbeilegung" })).toHaveAttribute("href", "#streitbeilegung");
    expect(within(toc).getAllByRole("link")).toHaveLength(screen.getAllByRole("heading", { level: 2 }).length);
  });

  it("says once, in the display language, that the German text is the one that counts", async () => {
    renderAt("/terms");
    await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 });
    expect(screen.getByText(/German text is the authoritative version/)).toBeInTheDocument();
    // The document itself is marked as German for a screen reader's voice.
    expect(screen.getByRole("heading", { level: 1 })).toHaveAttribute("lang", "de");
  });

  it("does not repeat it to a visitor who reads German anyway", async () => {
    localStorage.setItem("gx-lang", "de");
    renderAt("/terms");
    await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 });
    expect(screen.queryByText(/authoritative/)).not.toBeInTheDocument();
    expect(screen.queryByText(/maßgeblich/)).not.toBeInTheDocument();
  });

  it("carries the language picker, so the chrome can be read before any session", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    expect(screen.getByRole("combobox", { name: "Display language" })).toBeInTheDocument();
  });

  it("names the tab after the document, so /, /login and /privacy are three different tabs", async () => {
    const router = renderAt("/privacy");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const titles = [document.title];
    await router.navigate({ to: "/login" });
    await waitFor(() => expect(document.title).not.toBe(titles[0]));
    titles.push(document.title);
    await router.navigate({ to: "/" });
    await waitFor(() => expect(document.title).not.toBe(titles[1]));
    titles.push(document.title);
    expect(titles[0]).toBe("Datenschutzerklärung · Glyphoxa");
    expect(new Set(titles).size).toBe(3);
  });
});
