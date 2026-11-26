import { waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import {
  GetCurrentUserResponseSchema,
  GetUsageSummaryResponseSchema,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";

// Test helpers shared by the Setup screen's tests.

// The key row (SecretRow card) of a slot by its display name — "Groq",
// "ElevenLabs", "Bot token". Found by name rather than by the key field,
// because a slot the plan provides shows no field until the GM asks for one,
// and a saved slot shows the masked key instead.
export function keyRow(name: string): Promise<HTMLElement> {
  return waitFor(() => {
    const card = Array.from(document.querySelectorAll<HTMLElement>(".gx-provider-card")).find(
      (c) => c.querySelector(".gx-provider-row__name")?.firstChild?.textContent === name,
    );
    if (!card) throw new Error(`no key row named ${name}`);
    return card;
  });
}

// The two reads every Setup render makes beyond the key list, answered with a
// plain table on its own keys: the plan card and "Your table".
export const usageSummary = () => create(GetUsageSummaryResponseSchema, {});

export const currentUser = () =>
  create(GetCurrentUserResponseSchema, {
    user: create(UserSchema, { id: "u", name: "Rin" }),
    tenantName: "Rin's Table",
  });
