"use client";

import { useState, use } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Save, Trash2 } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Breadcrumbs } from "@/components/breadcrumbs";
import {
  useNPC,
  useCampaign,
  useUpdateNPC,
  useDeleteNPC,
} from "@/lib/hooks";
import type { NPC } from "@/lib/types";
import { NPCFormFields, type NPCFormState } from "../npc-form-fields";

interface NPCFormProps {
  npc: NPC;
  campaignId: string;
  campaignName: string;
}

function NPCForm({ npc, campaignId, campaignName }: NPCFormProps) {
  const router = useRouter();
  const updateNPC = useUpdateNPC(campaignId, npc.id);
  const deleteNPC = useDeleteNPC(campaignId);

  const [state, setState] = useState<NPCFormState>({
    name: npc.name,
    personality: npc.personality,
    voiceProvider: npc.voice_provider ?? npc.voice?.provider ?? '',
    voiceId: npc.voice_id ?? npc.voice?.voice_id ?? '',
    engine: npc.engine,
    budgetTier: npc.budget_tier,
    knowledgeScope: npc.knowledge_scope ?? [],
    behaviorRules: npc.behavior_rules ?? [],
    addressOnly: npc.address_only,
  });
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  const errors: Record<string, string> = {};
  if (!state.name.trim()) errors.name = "NPC name is required";
  if (!state.voiceId.trim()) errors.voiceId = "Voice ID is required";
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

    try {
      await updateNPC.mutateAsync({
        name: state.name.trim(),
        personality: state.personality.trim(),
        voice: { provider: state.voiceProvider, voice_id: state.voiceId.trim() },
        engine: state.engine,
        budget_tier: state.budgetTier,
        knowledge_scope: state.knowledgeScope,
        behavior_rules: state.behaviorRules,
        address_only: state.addressOnly,
      } as Partial<NPC>);
    } catch {
      // Error is handled by the mutation's onError callback.
    }
  }

  async function handleDelete() {
    try {
      await deleteNPC.mutateAsync(npc.id);
      router.push(`/campaigns/${campaignId}?tab=npcs`);
    } catch {
      // Error is handled by the mutation's onError callback.
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Campaigns", href: "/campaigns" },
          { label: campaignName, href: `/campaigns/${campaignId}` },
          { label: state.name || "NPC" },
        ]}
      />

      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">
          {state.name || "NPC"}
        </h1>
        <div className="flex gap-2">
          <Button variant="outline" render={<Link href={`/campaigns/${campaignId}`} />}>
            Discard
          </Button>
          <Button
            onClick={handleSave}
            disabled={updateNPC.isPending}
          >
            <Save className="mr-1 h-4 w-4" />
            {updateNPC.isPending ? "Saving..." : "Save Changes"}
          </Button>
        </div>
      </div>

      <NPCFormFields
        state={state}
        onChange={handleChange}
        touched={touched}
        errors={errors}
        onTouch={touch}
      />

      {/* Delete */}
      <Card className="border-destructive/50">
        <CardContent className="flex items-center justify-between p-4">
          <div>
            <p className="font-medium text-destructive">Delete NPC</p>
            <p className="text-sm text-muted-foreground">
              This action cannot be undone.
            </p>
          </div>
          <Dialog>
            <DialogTrigger render={<Button variant="destructive" size="sm" />}>
                <Trash2 className="mr-1 h-4 w-4" />
                Delete
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Delete NPC</DialogTitle>
                <DialogDescription>
                  Are you sure you want to delete &quot;{state.name}&quot;? This
                  cannot be undone.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button
                  variant="destructive"
                  onClick={handleDelete}
                  disabled={deleteNPC.isPending}
                >
                  {deleteNPC.isPending ? "Deleting..." : "Delete NPC"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </CardContent>
      </Card>
    </div>
  );
}

export default function NPCEditorPage({
  params,
}: {
  params: Promise<{ id: string; npcId: string }>;
}) {
  const { id: campaignId, npcId } = use(params);
  const { data: campaign } = useCampaign(campaignId);
  const { data: npc, isLoading } = useNPC(campaignId, npcId);

  if (isLoading) {
    return (
      <div className="mx-auto max-w-3xl space-y-4">
        <div className="h-4 w-48 rounded bg-muted skeleton-shimmer" />
        <div className="h-8 w-48 rounded bg-muted skeleton-shimmer" />
        <div className="h-96 rounded bg-muted skeleton-shimmer" />
      </div>
    );
  }

  if (!npc) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <p className="text-muted-foreground">NPC not found.</p>
        <Button variant="outline" render={<Link href={`/campaigns/${campaignId}`} />}>
          Back to Campaign
        </Button>
      </div>
    );
  }

  return (
    <NPCForm
      key={npc.id}
      npc={npc}
      campaignId={campaignId}
      campaignName={campaign?.name ?? "Campaign"}
    />
  );
}
