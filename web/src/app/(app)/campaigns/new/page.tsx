"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useCreateCampaign } from "@/lib/hooks";
import type { GameSystem } from "@/lib/types";

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

export default function NewCampaignPage() {
  const router = useRouter();
  const createCampaign = useCreateCampaign();
  const [name, setName] = useState("");
  const [gameSystem, setGameSystem] = useState<string>("");
  const [description, setDescription] = useState("");
  const [language, setLanguage] = useState("en");

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    await createCampaign.mutateAsync({
      name,
      game_system: gameSystem,
      description,
      language,
    });
    router.push("/campaigns");
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="icon" render={<Link href="/campaigns" />}>
            <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-2xl font-bold">New Campaign</h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Campaign Details</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="name">Campaign Name</Label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Die Chroniken von Rabenheim"
                required
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="system">Game System</Label>
              <Select value={gameSystem} onValueChange={(v) => v && setGameSystem(v)}>
                <SelectTrigger>
                  <SelectValue placeholder="Select a game system" />
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
              <Select value={language} onValueChange={(v) => v && setLanguage(v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="en">English</SelectItem>
                  <SelectItem value="de">Deutsch</SelectItem>
                  <SelectItem value="fr">Français</SelectItem>
                  <SelectItem value="es">Español</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Describe your campaign setting, story hooks, and key themes..."
                rows={6}
              />
            </div>

            <div className="flex justify-end gap-2">
              <Button variant="outline" render={<Link href="/campaigns" />}>
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={!name || createCampaign.isPending}
              >
                {createCampaign.isPending ? "Creating..." : "Create Campaign"}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
