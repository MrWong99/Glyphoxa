import { describe, it, expect } from "vitest";
import { render, screen, waitFor, fireEvent, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  AdmissionMode,
  GetAdmissionModeResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Login } from "./Login";

// backend fakes AuthService.GetAdmissionMode (the ADR-0055 deployment-posture
// probe — public, so it answers without a session). `seen.probed` records that
// the handler actually ran, which lets a test wait for the probe to settle even
// when the expected framing is the fail-safe default (no DOM change to await).
function backend(opts: { mode?: AdmissionMode; error?: boolean } = {}) {
  const seen = { probed: false };
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      getAdmissionMode: () => {
        seen.probed = true;
        if (opts.error) throw new ConnectError("posture probe down", Code.Unavailable);
        return create(GetAdmissionModeResponseSchema, {
          admissionMode: opts.mode ?? AdmissionMode.ALLOWLIST,
        });
      },
    });
  });
  return { transport, seen };
}

// renderLogin mounts the screen under Providers (Login now queries the
// admission mode over Connect) and waits for the probe to settle so react-query
// state updates land inside the test body.
async function renderLogin(ui: ReactElement, b = backend()) {
  render(
    <Providers transport={b.transport} queryClient={makeQueryClient()}>
      {ui}
    </Providers>,
  );
  await waitFor(() => expect(b.seen.probed).toBe(true));
}

describe("Login", () => {
  it("offers Continue with Discord pointing at the OAuth start", async () => {
    await renderLogin(<Login />);
    const link = screen.getByRole("link", { name: /continue with discord/i });
    expect(link).toHaveAttribute("href", "/auth/discord/login");
  });

  it("renders Google and GitHub as disabled coming-soon slots", async () => {
    await renderLogin(<Login />);
    expect(screen.getByRole("button", { name: /google/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /github/i })).toBeDisabled();
    expect(screen.getAllByText(/coming soon/i).length).toBeGreaterThanOrEqual(2);
  });

  it("shows a friendly not-authorized banner when notAuthorized is set", async () => {
    await renderLogin(<Login notAuthorized />);
    const banner = screen.getByRole("alert");
    // Mode-neutral and non-leaky: the same redirect covers an allowlist miss
    // AND an open-mode suspension, so the copy names neither cause.
    expect(banner).toHaveTextContent(/isn't authorized for this deployment/i);
    expect(banner).not.toHaveTextContent(/allowlist|suspend/i);
    // The Discord link stays available so the operator can retry with the right account.
    expect(screen.getByRole("link", { name: /continue with discord/i })).toBeInTheDocument();
  });

  it("renders no banner on a normal first visit", async () => {
    await renderLogin(<Login />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the allowlist framing when the deployment admission mode is allowlist", async () => {
    await renderLogin(<Login />, backend({ mode: AdmissionMode.ALLOWLIST }));
    expect(screen.getByText("Sign in to run your table.")).toBeInTheDocument();
    expect(screen.queryByText(/creates your own table/i)).not.toBeInTheDocument();
  });

  it("frames self-signup when the deployment admission mode is open", async () => {
    await renderLogin(<Login />, backend({ mode: AdmissionMode.OPEN }));
    expect(await screen.findByText(/creates your own table/i)).toBeInTheDocument();
    // ADR-0055 changes the copy; #518 additionally gates the OAuth start behind
    // the AUP acknowledgment — once ticked, the anchor is exactly as it was.
    fireEvent.click(screen.getByRole("checkbox"));
    const link = screen.getByRole("link", { name: /continue with discord/i });
    expect(link).toHaveAttribute("href", "/auth/discord/login");
  });

  it("requires the AUP acknowledgment before an open-mode signup can start", async () => {
    // #518: in open admission mode this screen IS the signup screen, so a
    // stranger cannot reach Discord OAuth without accepting the terms. The
    // blocked state is a real disabled button — an aria-disabled anchor would
    // still navigate on click.
    await renderLogin(<Login />, backend({ mode: AdmissionMode.OPEN }));
    await screen.findByText(/creates your own table/i);

    expect(screen.queryByRole("link", { name: /continue with discord/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /continue with discord/i })).toBeDisabled();

    const box = screen.getByRole("checkbox");
    expect(box).not.toBeChecked();
    fireEvent.click(box);

    expect(screen.getByRole("link", { name: /continue with discord/i })).toHaveAttribute(
      "href",
      "/auth/discord/login",
    );
    expect(screen.queryByRole("button", { name: /continue with discord/i })).not.toBeInTheDocument();
  });

  it("links the terms and the privacy policy from the acknowledgment", async () => {
    await renderLogin(<Login />, backend({ mode: AdmissionMode.OPEN }));
    await screen.findByText(/creates your own table/i);
    // Both documents are unauthenticated routes, so a visitor can read them
    // before deciding — that is the point of asking here. Scoped to the
    // acknowledgment: the always-present legal footer links them too.
    const label = screen.getByRole("checkbox").closest("label");
    expect(label).not.toBeNull();
    const consent = within(label as HTMLElement);
    expect(consent.getByRole("link", { name: "Nutzungsbedingungen" })).toHaveAttribute(
      "href",
      "/terms",
    );
    expect(consent.getByRole("link", { name: "Datenschutzerklärung" })).toHaveAttribute(
      "href",
      "/privacy",
    );
  });

  it("asks for no acknowledgment in allowlist mode", async () => {
    // An allowlisted operator agreed to their own deployment's terms by running
    // it; the pre-#518 flow is unchanged for them.
    await renderLogin(<Login />, backend({ mode: AdmissionMode.ALLOWLIST }));
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /continue with discord/i })).toBeInTheDocument();
  });

  it("shows the legal footer to a visitor with no session", async () => {
    await renderLogin(<Login />);
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
  });

  it("falls back to the allowlist framing when the admission probe errors", async () => {
    await renderLogin(<Login />, backend({ error: true }));
    expect(screen.getByText("Sign in to run your table.")).toBeInTheDocument();
    expect(screen.queryByText(/creates your own table/i)).not.toBeInTheDocument();
  });
});
