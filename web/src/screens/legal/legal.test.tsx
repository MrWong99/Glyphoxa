import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
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
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
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
