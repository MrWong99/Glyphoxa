import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";

import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";

// A deployment reached DIRECTLY — no tunnel, no CDN, no third-party proxy in
// front — must not name one as a data recipient. Naming a processor that never
// sees the traffic is not a harmless over-disclosure: it is a false statement
// about where the data goes.
//
// The shipped operator.ts declares Cloudflare (the canonical deployment runs
// behind a Cloudflare Tunnel), so this case can only be exercised against a
// mocked identity — the mirror of todoBanner.test.tsx.
vi.mock("./operator", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./operator")>();
  const direct = { ...actual.OPERATOR, edgeProvider: "" };
  // operatorTodos' default argument binds the real module's OPERATOR, so it
  // must be re-bound to the mocked identity for LegalPage's no-arg call.
  return {
    ...actual,
    OPERATOR: direct,
    operatorTodos: (o = direct) => actual.operatorTodos(o),
  };
});

function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <Providers transport={createRouterTransport(() => {})} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
}

describe("Datenschutzerklärung without an edge provider", () => {
  it("names no reverse-proxy recipient when the operator declares none", async () => {
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const doc = document.body.textContent ?? "";
    expect(doc).not.toContain("Cloudflare");
    expect(doc).not.toContain("TLS-Terminierung");
  });

  it("still discloses the providers the software itself uses", async () => {
    // The proxy in front is a deployment choice; the STT/TTS/LLM subprocessors
    // are a property of the software. Dropping the first must not drop the rest
    // of section 5.
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    const doc = document.body.textContent ?? "";
    for (const topic of ["Discord", "ElevenLabs", "Groq", "Gemini"]) {
      expect(doc, `Datenschutzerklärung must still mention ${topic}`).toContain(topic);
    }
  });

  it("treats an empty edge provider as complete, not as an operator TODO", async () => {
    // Self-hosting directly is the normal case, not an unfinished Impressum —
    // it must not raise the banner that means "you forgot something".
    renderAt("/datenschutz");
    await screen.findByRole("heading", { name: "Datenschutzerklärung", level: 1 });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
