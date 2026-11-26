import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast } from "sonner";

import {
  AuthService,
  GetCurrentUserResponseSchema,
  RenameTenantResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { TableCard } from "./TableCard";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

function mount(renames: string[] = []) {
  let name = "Rin's Table";
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => create(GetCurrentUserResponseSchema, { tenantName: name }),
      renameTenant: (req) => {
        renames.push(req.name);
        name = req.name;
        return create(RenameTenantResponseSchema, { tenantName: name });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <TableCard />
    </Providers>,
  );
}

describe("TableCard", () => {
  it("renames the table, with Save off until the name changed, and toasts", async () => {
    const renames: string[] = [];
    mount(renames);
    const field = (await screen.findByLabelText("Table name")) as HTMLInputElement;
    expect(field.value).toBe("Rin's Table");
    const save = screen.getByRole("button", { name: "Rename table" });
    expect(save).toBeDisabled();

    fireEvent.change(field, { target: { value: "  Saltmarsh Crew " } });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(renames).toEqual(["Saltmarsh Crew"]));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Table renamed to Saltmarsh Crew"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Rename table" })).toBeDisabled());
  });

  it("keeps a blank name from being saved", async () => {
    mount();
    fireEvent.change(await screen.findByLabelText("Table name"), { target: { value: "   " } });
    expect(screen.getByRole("button", { name: "Rename table" })).toBeDisabled();
  });

  it("sends deletion to the operator instead of offering it", async () => {
    mount();
    const link = await screen.findByRole("link", { name: "write to the operator" });
    expect(link.getAttribute("href")).toMatch(/^(mailto:|\/imprint)/);
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });
});
