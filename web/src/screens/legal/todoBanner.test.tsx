import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";

import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "@/app/router";

// The shipped operator.ts is filled (the canonical deployment's identity), so
// the unfilled-identity path can no longer be exercised against the real
// module. This suite mocks an UNFILLED identity to keep the safety mechanism
// covered: a deployment with placeholder fields must scream at its operator.
vi.mock("./operator", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./operator")>();
  const unfilled = {
    ...actual.OPERATOR,
    legalName: "[BITTE AUSFÜLLEN: vollständiger Name des Betreibers]",
    email: "[BITTE AUSFÜLLEN: Kontakt-E-Mail-Adresse]",
  };
  // operatorTodos' default argument binds the real module's OPERATOR, so it
  // must be re-bound to the mocked identity for LegalPage's no-arg call.
  return {
    ...actual,
    OPERATOR: unfilled,
    operatorTodos: (o = unfilled) => actual.operatorTodos(o),
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

describe("operator TODO banner (unfilled identity)", () => {
  it("shows a loud operator TODO while any required field is a placeholder", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    expect(screen.getByRole("alert")).toHaveTextContent(/Hinweis an den Betreiber/i);
    expect(screen.getByRole("alert")).toHaveTextContent(/legalName/);
  });

  it("marks each unfilled value inline", async () => {
    renderAt("/imprint");
    await screen.findByRole("heading", { name: "Impressum", level: 1 });
    const placeholders = document.querySelectorAll(".gx-legal__placeholder");
    expect(placeholders.length).toBeGreaterThan(0);
  });
});
