"use client";

import { use, useState } from "react";
import { useRouter } from "next/navigation";
import { Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useCampaign, useCreateNPC } from "@/lib/hooks";
import type { NPC } from "@/lib/types";
import { NPCFormFields, type NPCFormState } from "../npc-form-fields";

export default function NewNPCPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id: campaignId } = use(params);
  const router = useRouter();
  const { data: campaign } = useCampaign(campaignId);
  const createNPC = useCreateNPC(campaignId);

  const [state, setState] = useState<NPCFormState>({
    name: "",
    personality: "",
    voiceProvider: "elevenlabs",
    voiceId: "",
    engine: "cascaded",
    budgetTier: "standard",
    knowledgeScope: [],
    behaviorRules: [],
    addressOnly: false,
  });
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  const errors: Record<string, string> = {};
  if (!state.name.trim()) errors.name = "NPC name is required";
  if (!state.voiceId.trim()) errors.voiceId = "Voice ID is required";
  if (!state.personality.trim()) errors.personality = "Personality description is required";
  if (state.personality.length > 2000) errors.personality = "Personality must be under 2000 characters";

  function handleChange<K extends keyof NPCFormState>(key: K, value: NPCFormState[K]) {
    setState((prev) => ({ ...prev, [key]: value }));
  }

  function touch(field: string) {
    setTouched((prev) => ({ ...prev, [field]: true }));
  }

  async function handleSave() {
    setTouched({ name: true, voiceId: true, personality: true });
    if (Object.keys(errors).length > 0) return;
    await createNPC.mutateAsync({
      name: state.name.trim(),
      personality: state.personality.trim(),
      voice: { provider: state.voiceProvider, voice_id: state.voiceId.trim() },
      engine: state.engine,
      budget_tier: state.budgetTier,
      knowledge_scope: state.knowledgeScope,
      behavior_rules: state.behaviorRules,
      address_only: state.addressOnly,
    } as Partial<NPC>);
    router.push(`/campaigns/${campaignId}`);
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Campaigns", href: "/campaigns" },
          { label: campaign?.name ?? "Campaign", href: `/campaigns/${campaignId}` },
          { label: "New NPC" },
        ]}
      />

      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">New NPC</h1>
        <Button onClick={handleSave} disabled={createNPC.isPending}>
          <Save className="mr-1 h-4 w-4" />
          {createNPC.isPending ? "Creating..." : "Create NPC"}
        </Button>
      </div>

      <NPCFormFields
        state={state}
        onChange={handleChange}
        touched={touched}
        errors={errors}
        onTouch={touch}
      />
    </div>
  );
}
