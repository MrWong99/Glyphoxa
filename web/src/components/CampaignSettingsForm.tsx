import { useState } from "react";
import type { FormEvent } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService, type Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { Button } from "@/components/ui/Button";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";

import "./createCampaignForm.css";

// The per-campaign settings editor (#268): edit an existing campaign's name,
// System, and Campaign Language, rendered in the topbar CampaignSwitcher's edit
// panel (#266). It mirrors CreateCampaignForm's plain-useState shape (ADR-0017),
// but the two curated fields diverge per the recorded product decisions:
//
//   System   — free text with datalist suggestions (no enum, rides the wire
//              opaque server-side, ADR-0039). The suggestions are a static
//              web-side convenience only.
//   Language — a SELECT constrained to the registered phonetic encoders
//              (ListSupportedLanguages → ADR-0024 EncoderRegistry, the sole
//              language-truth source), so a new encoder appears automatically and
//              no language list is hardcoded in the web tier.
//
// A language change mutates NOTHING now — existing Agents' voice settings are
// untouched (ADR-0009, #224); it takes effect on the next Voice Session, which
// the static hint under the select states.

const SYSTEM_SUGGESTIONS = ["D&D 5e", "Pathfinder 2e", "Call of Cthulhu"];
const SYSTEM_DATALIST_ID = "gx-system-suggestions";

// languageLabel renders a code as "<English name> (<code>)" via Intl, falling
// back to the bare code when the runtime can't name it — so the option list
// stays readable without a hardcoded language table.
function languageLabel(code: string): string {
  try {
    const name = new Intl.DisplayNames(["en"], { type: "language" }).of(code);
    return name && name !== code ? `${name} (${code})` : code;
  } catch {
    return code;
  }
}

export function CampaignSettingsForm({
  campaign,
  onSaved,
  onCancel,
}: {
  campaign: Campaign;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(campaign.name);
  const [system, setSystem] = useState(campaign.system);
  const [language, setLanguage] = useState(campaign.language);

  // The Campaign Language choices come solely from the registered encoders.
  const langQ = useQuery(CampaignService.method.listSupportedLanguages, {});
  const supported = langQ.data?.languages ?? [];

  const options = supported.map((code) => ({ value: code, label: languageLabel(code) }));
  // A stored language with no registered encoder still rides here as an extra
  // option so the SELECT can't silently coerce it to a supported one on save.
  if (campaign.language && !supported.includes(campaign.language)) {
    options.push({ value: campaign.language, label: `${languageLabel(campaign.language)} (unsupported)` });
  }

  // listCampaigns is campaign-INVARIANT (lib/campaignCache.ts), so the sweep
  // skips it — a name/system edit must invalidate it explicitly or the switcher's
  // picker keeps showing the stale name/system.
  const invalidateList = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCampaigns,
        cardinality: "finite",
      }),
    });

  const update = useMutation(CampaignService.method.updateCampaign, {
    onSuccess: () => {
      void invalidateList();
      void invalidateActiveCampaignScopedQueries(queryClient);
      onSaved();
    },
    onError: (err) => toast.error(`Couldn't save campaign settings: ${err.message}`),
  });

  const canSubmit = name.trim() !== "" && !update.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    // Name is trimmed (the server rejects empty); System/Language ride opaque.
    update.mutate({ id: campaign.id, name: name.trim(), system, language });
  };

  return (
    <form className="gx-campaign-create" onSubmit={submit}>
      <Input
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="e.g. The Sunless Citadel"
        disabled={update.isPending}
        required
      />
      <div className="gx-campaign-create__row">
        <Input
          label="System"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          list={SYSTEM_DATALIST_ID}
          hint="Free-text — e.g. D&D 5e, Pathfinder 2e"
          disabled={update.isPending}
        />
        <datalist id={SYSTEM_DATALIST_ID}>
          {SYSTEM_SUGGESTIONS.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
        <div className="gx-campaign-create__lang">
          <Select
            label="Language"
            options={options}
            value={language}
            onValueChange={setLanguage}
            disabled={update.isPending}
          />
          <span className="gx-field__hint">Takes effect on the next Voice Session.</span>
        </div>
      </div>
      <div className="gx-campaign-create__actions">
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {update.isPending ? "Saving…" : "Save changes"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={update.isPending}>
          Cancel
        </Button>
        {update.error && (
          <span className="gx-campaign-create__error" role="alert">
            Couldn&apos;t save: {update.error.message}
          </span>
        )}
      </div>
    </form>
  );
}
