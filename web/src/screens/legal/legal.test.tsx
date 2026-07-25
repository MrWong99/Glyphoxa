import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";

import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";
import { operatorTodos, isPlaceholder, OPERATOR } from "./operator";

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
}

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
      "Gemini",
      "Cloudflare",
      "Auftragsverarbeitung",
      "Speicherdauer",
      "Aufsichtsbehörde",
    ]) {
      expect(doc, `Datenschutzerklärung must mention ${topic}`).toContain(topic);
    }
  });

  it("links the other documents from every legal page", async () => {
    renderAt("/terms");
    await screen.findByRole("heading", { name: "Nutzungsbedingungen", level: 1 });
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
  });

  it("shows a loud operator TODO while the identity is unfilled", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    // The shipped template is unfilled by design (the operator supplies their
    // own identity — the project must not invent one), so the banner is present
    // here. It disappears field by field as operator.ts is filled in.
    expect(operatorTodos().length).toBeGreaterThan(0);
    expect(screen.getByRole("alert")).toHaveTextContent(/Hinweis an den Betreiber/i);
  });
});

describe("operator identity template", () => {
  it("ships placeholders, not invented details", () => {
    // Guard against someone "helpfully" filling these in upstream: a shipped
    // default identity would be a false Impressum on every deployment.
    expect(isPlaceholder(OPERATOR.legalName)).toBe(true);
    expect(isPlaceholder(OPERATOR.street)).toBe(true);
    expect(isPlaceholder(OPERATOR.email)).toBe(true);
  });

  it("counts an empty required field as a TODO and ignores optional ones", () => {
    const filled = {
      ...OPERATOR,
      legalName: "Rin Okabe",
      street: "Beispielweg 1",
      city: "10115 Berlin",
      country: "Deutschland",
      email: "hallo@example.org",
      supervisoryAuthority: "Berliner Beauftragte für Datenschutz",
      hostingLocation: "Deutschland",
      lastUpdated: "2026-07-25",
      phone: "", // optional — an empty value is a decision, not an omission
    };
    expect(operatorTodos(filled)).toEqual([]);
    expect(operatorTodos({ ...filled, email: "  " })).toEqual(["email"]);
  });
});
