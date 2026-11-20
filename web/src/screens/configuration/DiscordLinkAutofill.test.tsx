import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import { ProviderService, ResolveGuildInviteResponseSchema } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { DiscordLinkAutofill } from "./DiscordLinkAutofill";

function mount() {
  const onFill = vi.fn();
  const transport = createRouterTransport(({ service }) => {
    service(ProviderService, {
      resolveGuildInvite: () =>
        create(ResolveGuildInviteResponseSchema, { guildId: "111", guildName: "The Keep" }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <DiscordLinkAutofill onFill={onFill} />
    </Providers>,
  );
  return { onFill, input: screen.getByLabelText(/paste a discord link/i) };
}

// W15: the "Server ID filled in from <server>" line described the INVITE that
// was resolved; it stayed up after the id had been replaced from another link.
describe("DiscordLinkAutofill resolved-server confirmation", () => {
  it("drops the confirmation when a channel link then fills a different server", async () => {
    const { onFill, input } = mount();
    fireEvent.change(input, { target: { value: "https://discord.gg/abc123" } });
    expect(await screen.findByTestId("resolved-guild")).toHaveTextContent("The Keep");

    fireEvent.change(input, { target: { value: "https://discord.com/channels/123456789012345678/234567890123456789" } });
    await waitFor(() => expect(onFill).toHaveBeenLastCalledWith("123456789012345678"));
    expect(screen.queryByTestId("resolved-guild")).not.toBeInTheDocument();
  });

  it("drops the confirmation when the link is cleared", async () => {
    const { input } = mount();
    fireEvent.change(input, { target: { value: "https://discord.gg/abc123" } });
    await screen.findByTestId("resolved-guild");

    fireEvent.change(input, { target: { value: "" } });
    expect(screen.queryByTestId("resolved-guild")).not.toBeInTheDocument();
  });
});
