"use client";

import { useState, use } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Save, Trash2, Users, ScrollText, Network } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { useCampaign, useUpdateCampaign, useDeleteCampaign, useHasRole } from "@/lib/hooks";
import { NPCList } from "./npcs/npc-list";
import { CampaignSessions } from "./sessions";
import type { Campaign, GameSystem } from "@/lib/types";

const gameSystems: GameSystem[] = [
  "D&D 5e",
  "D&D 5e (2024)",
  "Pathfinder 2e",
  "Das Schwarze Auge",
  "Call of Cthulhu",
  "Shadowrun",
  "Fate Core",
  "Savage Worlds",
  "Other",
];

function CampaignForm({ campaign, campaignId }: { campaign: Campaign; campaignId: string }) {
  const router = useRouter();
  const updateCampaign = useUpdateCampaign(campaignId);
  const deleteCampaign = useDeleteCampaign();
  const [deleteConfirm, setDeleteConfirm] = useState("");
  const canEdit = useHasRole("dm");

  const [name, setName] = useState(campaign.name);
  const [gameSystem, setGameSystem] = useState(campaign.game_system);
  const [description, setDescription] = useState(campaign.description);
  const [language, setLanguage] = useState(campaign.language);
  const [dirty, setDirty] = useState(false);

  function handleChange<T>(setter: (v: T) => void) {
    return (v: T) => {
      setter(v);
      setDirty(true);
    };
  }

  async function handleSave() {
    await updateCampaign.mutateAsync({
      name,
      game_system: gameSystem,
      description,
      language,
    });
    setDirty(false);
  }

  async function handleDelete() {
    await deleteCampaign.mutateAsync(campaignId);
    router.push("/campaigns");
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Campaigns", href: "/campaigns" },
          { label: campaign.name },
        ]}
      />

      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">{campaign.name}</h1>
        {canEdit && dirty && (
          <Button onClick={handleSave} disabled={updateCampaign.isPending}>
            <Save className="mr-1 h-4 w-4" />
            {updateCampaign.isPending ? "Saving..." : "Save Changes"}
          </Button>
        )}
      </div>

      <Tabs defaultValue="details">
        <TabsList>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="npcs">
            <Users className="mr-1 h-4 w-4" />
            NPCs
          </TabsTrigger>
          <TabsTrigger value="sessions">
            <ScrollText className="mr-1 h-4 w-4" />
            Sessions
          </TabsTrigger>
          <TabsTrigger value="knowledge" render={<Link href={`/campaigns/${campaignId}/knowledge`} />}>
            <Network className="mr-1 h-4 w-4" />
            Knowledge Graph
          </TabsTrigger>
        </TabsList>

        <TabsContent value="details" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Campaign Details</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">Campaign Name</Label>
                <Input
                  id="name"
                  value={name}
                  onChange={(e) => handleChange(setName)(e.target.value)}
                  readOnly={!canEdit}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="system">Game System</Label>
                <Select
                  value={gameSystem}
                  onValueChange={(v) => v && handleChange(setGameSystem)(v)}
                  disabled={!canEdit}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {gameSystems.map((system) => (
                      <SelectItem key={system} value={system}>
                        {system}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="language">Language</Label>
                <Select
                  value={language}
                  onValueChange={(v) => v && handleChange(setLanguage)(v)}
                  disabled={!canEdit}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="en">English</SelectItem>
                    <SelectItem value="de">Deutsch</SelectItem>
                    <SelectItem value="fr">Fran&ccedil;ais</SelectItem>
                    <SelectItem value="es">Espa&ntilde;ol</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="description">Description</Label>
                <Textarea
                  id="description"
                  value={description}
                  onChange={(e) =>
                    handleChange(setDescription)(e.target.value)
                  }
                  rows={8}
                  readOnly={!canEdit}
                />
              </div>
            </CardContent>
          </Card>

          {/* Danger zone — only visible for DM+ roles */}
          {canEdit && (
            <Card className="border-destructive/50">
              <CardHeader>
                <CardTitle className="text-destructive">Danger Zone</CardTitle>
              </CardHeader>
              <CardContent>
                <Dialog>
                  <DialogTrigger render={<Button variant="destructive" />}>
                      <Trash2 className="mr-1 h-4 w-4" />
                      Delete Campaign
                  </DialogTrigger>
                  <DialogContent>
                    <DialogHeader>
                      <DialogTitle>Delete Campaign</DialogTitle>
                      <DialogDescription>
                        This action cannot be undone. This will permanently delete
                        the campaign &quot;{campaign.name}&quot; and all its NPCs,
                        sessions, and transcripts.
                      </DialogDescription>
                    </DialogHeader>
                    <div className="space-y-2">
                      <Label>
                        Type <strong>{campaign.name}</strong> to confirm
                      </Label>
                      <Input
                        value={deleteConfirm}
                        onChange={(e) => setDeleteConfirm(e.target.value)}
                        placeholder={campaign.name}
                      />
                    </div>
                    <DialogFooter>
                      <Button
                        variant="destructive"
                        disabled={
                          deleteConfirm !== campaign.name ||
                          deleteCampaign.isPending
                        }
                        onClick={handleDelete}
                      >
                        {deleteCampaign.isPending
                          ? "Deleting..."
                          : "Delete Campaign"}
                      </Button>
                    </DialogFooter>
                  </DialogContent>
                </Dialog>
              </CardContent>
            </Card>
          )}
        </TabsContent>

        <TabsContent value="npcs">
          <NPCList campaignId={campaignId} />
        </TabsContent>

        <TabsContent value="sessions">
          <CampaignSessions campaignId={campaignId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

export default function CampaignDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { data: campaign, isLoading } = useCampaign(id);

  if (isLoading) {
    return (
      <div className="mx-auto max-w-4xl space-y-4">
        <div className="h-4 w-48 rounded bg-muted skeleton-shimmer" />
        <div className="h-8 w-64 rounded bg-muted skeleton-shimmer" />
        <div className="h-64 rounded bg-muted skeleton-shimmer" />
      </div>
    );
  }

  if (!campaign) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <p className="text-muted-foreground">Campaign not found.</p>
        <Button variant="outline" render={<Link href="/campaigns" />}>
          Back to Campaigns
        </Button>
      </div>
    );
  }

  return <CampaignForm key={campaign.id} campaign={campaign} campaignId={id} />;
}
