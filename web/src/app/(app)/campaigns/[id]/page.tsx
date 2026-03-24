"use client";

import { useState, use } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Save, Trash2, Users, ScrollText } from "lucide-react";
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
import { useCampaign, useUpdateCampaign, useDeleteCampaign } from "@/lib/hooks";
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
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" aria-label="Back to campaigns" render={<Link href="/campaigns" />}>
              <ArrowLeft className="h-4 w-4" />
          </Button>
          <h1 className="text-2xl font-bold">{campaign.name}</h1>
        </div>
        {dirty && (
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
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="system">Game System</Label>
                <Select
                  value={gameSystem}
                  onValueChange={(v) => v && handleChange(setGameSystem)(v)}
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
                />
              </div>
            </CardContent>
          </Card>

          {/* Danger zone */}
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
      <div className="mx-auto max-w-4xl animate-pulse space-y-4">
        <div className="h-8 w-48 rounded bg-muted" />
        <div className="h-64 rounded bg-muted" />
      </div>
    );
  }

  if (!campaign) {
    return (
      <div className="text-center">
        <p className="text-muted-foreground">Campaign not found.</p>
      </div>
    );
  }

  return <CampaignForm key={campaign.id} campaign={campaign} campaignId={id} />;
}
