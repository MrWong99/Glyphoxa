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

  it("offers Discord only — no permanently disabled coming-soon providers", async () => {
    await renderLogin(<Login />);
    expect(screen.queryByRole("button", { name: /google/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /github/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/coming soon/i)).not.toBeInTheDocument();
  });

  it("shows a friendly not-authorized banner when notAuthorized is set", async () => {
    await renderLogin(<Login notAuthorized />);
    const banner = screen.getByRole("alert");
    // Mode-neutral and non-leaky: the same redirect covers an allowlist miss
    // AND an open-mode suspension, so the copy names neither cause.
    expect(banner).toHaveTextContent(/isn't authorized here/i);
    expect(banner).not.toHaveTextContent(/allowlist|suspend/i);
    // The Discord link stays available so the operator can retry with the right account.
    expect(screen.getByRole("link", { name: /continue with discord/i })).toBeInTheDocument();
  });

  // ADR-0065: an instance that admits by Access Grant can say how to get in.
  it("says an invite-only instance is invite-only, and to ask its admin for an invitation link", async () => {
    await renderLogin(<Login notAuthorized />, backend({ mode: AdmissionMode.ALLOWLIST }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "This Discord account isn't authorized here. This instance is invite-only — ask its admin for an invitation link.",
      ),
    );
    // Still non-leaky: nothing about why THIS account was refused.
    expect(screen.getByRole("alert")).not.toHaveTextContent(/allowlist|suspend|revoked/i);
  });

  it("does not call an open instance invite-only: a refusal there keeps the bare sentence", async () => {
    // In open mode the same redirect means a suspended account; "invite-only"
    // would be false.
    await renderLogin(<Login notAuthorized />, backend({ mode: AdmissionMode.OPEN }));
    await screen.findByText(/creates your own table/i);
    const banner = screen.getByRole("alert");
    expect(banner).toHaveTextContent("This Discord account isn't authorized here.");
    expect(banner).not.toHaveTextContent(/invite-only|invitation link/i);
  });

  it("does not claim invite-only while the Admission Mode is unknown", async () => {
    await renderLogin(<Login notAuthorized />, backend({ error: true }));
    await waitFor(() => expect(screen.getByRole("link", { name: /continue with discord/i })).toBeInTheDocument());
    expect(screen.getByRole("alert")).not.toHaveTextContent(/invite-only/i);
  });

  it("says it in German too", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      await renderLogin(<Login notAuthorized />, backend({ mode: AdmissionMode.ALLOWLIST }));
      await waitFor(() =>
        expect(screen.getByRole("alert")).toHaveTextContent(
          /nur auf Einladung zugänglich — bitte ihren Admin um einen Einladungslink/,
        ),
      );
    } finally {
      localStorage.removeItem("gx-lang");
    }
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
    // the AUP acknowledgment — once ticked, the anchor carries the ?aup=1 the
    // server-side gate requires for an open-mode start (Go↔TS contract with
    // internal/auth/oauth.go).
    fireEvent.click(screen.getByRole("checkbox"));
    const link = screen.getByRole("link", { name: /continue with discord/i });
    expect(link).toHaveAttribute("href", "/auth/discord/login?aup=1");
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
      "/auth/discord/login?aup=1",
    );
    expect(screen.queryByRole("button", { name: /continue with discord/i })).not.toBeInTheDocument();
  });

  it("explains the bounce when the server-side AUP gate refused the start", async () => {
    // #518's backstop: a hand-typed /auth/discord/login in open mode 302s back
    // here with ?error=aup_required — the screen says why instead of silently
    // showing the same form again.
    await renderLogin(<Login aupRequired />, backend({ mode: AdmissionMode.OPEN }));
    expect(screen.getByRole("alert")).toHaveTextContent(/accept the Nutzungsbedingungen/i);
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

describe("Login: a way back and a reason", () => {
  it("names the browser tab, and hands it back on the way out", async () => {
    document.title = "Glyphoxa";
    const { unmount } = render(
      <Providers transport={backend().transport} queryClient={makeQueryClient()}>
        <Login />
      </Providers>,
    );
    expect(document.title).toBe("Sign in · Glyphoxa");
    unmount();
    expect(document.title).toBe("Glyphoxa");
  });

  it("leads back to the start page from the wordmark and from 'What is Glyphoxa?'", async () => {
    await renderLogin(<Login />);
    // The wordmark is still the page's one heading, and now inside a link home.
    const home = screen.getByRole("link", { name: "Glyphoxa — start page" });
    expect(home).toHaveAttribute("href", "/");
    expect(within(home).getByRole("heading", { level: 1, name: "Glyphoxa" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "What is Glyphoxa?" })).toHaveAttribute("href", "/");
  });

  it("says why the Discord button is not available, read with the button itself", async () => {
    await renderLogin(<Login />, backend({ mode: AdmissionMode.OPEN }));
    await screen.findByText(/creates your own table/i);

    const blocked = screen.getByRole("button", { name: /continue with discord/i });
    expect(blocked).toBeDisabled();
    expect(blocked).toHaveAccessibleDescription("Tick the box above to continue.");

    // Ticked: the start is a link again, and the reason is gone.
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("link", { name: /continue with discord/i })).not.toHaveAttribute("aria-describedby");
    expect(screen.queryByText("Tick the box above to continue.")).not.toBeInTheDocument();
  });

  it("says the button is waiting on the server while the admission probe is in flight", () => {
    // A probe that never answers: the start stays disabled (a fast click must
    // not slip past an open-mode acknowledgment), and it says why.
    const transport = createRouterTransport(({ service }) => {
      service(AuthService, { getAdmissionMode: () => new Promise(() => {}) });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Login />
      </Providers>,
    );
    const blocked = screen.getByRole("button", { name: /continue with discord/i });
    expect(blocked).toBeDisabled();
    expect(blocked).toHaveAccessibleDescription("Checking how sign-in works on this server…");
  });

  it("needs no reason when the start is open in allowlist mode", async () => {
    await renderLogin(<Login />, backend({ mode: AdmissionMode.ALLOWLIST }));
    expect(screen.getByRole("link", { name: /continue with discord/i })).not.toHaveAttribute("aria-describedby");
  });

  it("keeps the language picker on the page", async () => {
    await renderLogin(<Login />);
    expect(screen.getByRole("combobox", { name: "Display language" })).toBeInTheDocument();
  });
});
