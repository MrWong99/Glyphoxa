"use client";

import { useState, useEffect, use } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Save, Trash2, Plus, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
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
import {
  useNPC,
  useCampaign,
  useUpdateNPC,
  useCreateNPC,
  useDeleteNPC,
} from "@/lib/hooks";

interface NPCEditorPageProps {
  params: Promise<{ id: string; npcId: string }>;
}

export default function NPCEditorPage({ params }: NPCEditorPageProps) {
  const { id: campaignId, npcId } = use(params);
  const isNew = npcId === "new";
  const router = useRouter();
  const { data: campaign } = useCampaign(campaignId);
  const { data: npc, isLoading } = useNPC(campaignId, isNew ? "" : npcId);
  const updateNPC = useUpdateNPC(campaignId, npcId);
  const createNPC = useCreateNPC(campaignId);
  const deleteNPC = useDeleteNPC(campaignId);

  const [name, setName] = useState("");
  const [personality, setPersonality] = useState("");
  const [voiceProvider, setVoiceProvider] = useState("elevenlabs");
  const [voiceId, setVoiceId] = useState("");
  const [engine, setEngine] = useState<"cascaded" | "s2s" | "sentence">(
    "cascaded",
  );
  const [budgetTier, setBudgetTier] = useState<"fast" | "standard" | "deep">(
    "standard",
  );
  const [knowledgeScope, setKnowledgeScope] = useState<string[]>([]);
  const [behaviorRules, setBehaviorRules] = useState<string[]>([]);
  const [addressOnly, setAddressOnly] = useState(false);
  const [newKnowledge, setNewKnowledge] = useState("");
  const [newRule, setNewRule] = useState("");
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (npc) {
      setName(npc.name);
      setPersonality(npc.personality);
      setVoiceProvider(npc.voice_provider);
      setVoiceId(npc.voice_id);
      setEngine(npc.engine);
      setBudgetTier(npc.budget_tier);
      setKnowledgeScope(npc.knowledge_scope ?? []);
      setBehaviorRules(npc.behavior_rules ?? []);
      setAddressOnly(npc.address_only);
      setDirty(false);
    }
  }, [npc]);

  function markDirty() {
    setDirty(true);
  }

  async function handleSave() {
    const data = {
      name,
      personality,
      voice_provider: voiceProvider,
      voice_id: voiceId,
      engine,
      budget_tier: budgetTier,
      knowledge_scope: knowledgeScope,
      behavior_rules: behaviorRules,
      address_only: addressOnly,
    };

    if (isNew) {
      await createNPC.mutateAsync(data);
    } else {
      await updateNPC.mutateAsync(data);
    }
    setDirty(false);
    if (isNew) {
      router.push(`/campaigns/${campaignId}`);
    }
  }

  async function handleDelete() {
    await deleteNPC.mutateAsync(npcId);
    router.push(`/campaigns/${campaignId}`);
  }

  function addKnowledge() {
    if (newKnowledge.trim()) {
      setKnowledgeScope([...knowledgeScope, newKnowledge.trim()]);
      setNewKnowledge("");
      markDirty();
    }
  }

  function removeKnowledge(index: number) {
    setKnowledgeScope(knowledgeScope.filter((_, i) => i !== index));
    markDirty();
  }

  function addRule() {
    if (newRule.trim()) {
      setBehaviorRules([...behaviorRules, newRule.trim()]);
      setNewRule("");
      markDirty();
    }
  }

  function removeRule(index: number) {
    setBehaviorRules(behaviorRules.filter((_, i) => i !== index));
    markDirty();
  }

  if (!isNew && isLoading) {
    return (
      <div className="mx-auto max-w-3xl animate-pulse space-y-4">
        <div className="h-8 w-48 rounded bg-muted" />
        <div className="h-96 rounded bg-muted" />
      </div>
    );
  }

  const isPending = updateNPC.isPending || createNPC.isPending;

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" render={<Link href={`/campaigns/${campaignId}`} />}>
              <ArrowLeft className="h-4 w-4" />
          </Button>
          <div>
            <p className="text-sm text-muted-foreground">
              {campaign?.name ?? "Campaign"} / NPCs
            </p>
            <h1 className="text-2xl font-bold">
              {isNew ? "New NPC" : name || "NPC"}
            </h1>
          </div>
        </div>
        <div className="flex gap-2">
          {!isNew && (
            <Button variant="outline" render={<Link href={`/campaigns/${campaignId}`} />}>
              Discard
            </Button>
          )}
          <Button
            onClick={handleSave}
            disabled={!name || isPending}
          >
            <Save className="mr-1 h-4 w-4" />
            {isPending ? "Saving..." : isNew ? "Create NPC" : "Save Changes"}
          </Button>
        </div>
      </div>

      {/* Identity */}
      <Card>
        <CardHeader>
          <CardTitle>Identity</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="npc-name">Name</Label>
            <Input
              id="npc-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                markDirty();
              }}
              placeholder="Heinrich der Wächter"
            />
          </div>
        </CardContent>
      </Card>

      {/* Personality */}
      <Card>
        <CardHeader>
          <CardTitle>Personality</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <Textarea
            value={personality}
            onChange={(e) => {
              setPersonality(e.target.value);
              markDirty();
            }}
            placeholder="A stern but fair city guard who has watched the gates of Rabenheim for over 20 years. He knows every resident by name and is suspicious of strangers..."
            rows={6}
          />
          <p className="text-xs text-muted-foreground">
            {personality.length} / 2000 characters
          </p>
        </CardContent>
      </Card>

      {/* Voice */}
      <Card>
        <CardHeader>
          <CardTitle>Voice</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select
                value={voiceProvider}
                onValueChange={(v) => {
                  if (v) setVoiceProvider(v);
                  markDirty();
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="elevenlabs">ElevenLabs</SelectItem>
                  <SelectItem value="azure">Azure</SelectItem>
                  <SelectItem value="google">Google</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="voice-id">Voice ID</Label>
              <Input
                id="voice-id"
                value={voiceId}
                onChange={(e) => {
                  setVoiceId(e.target.value);
                  markDirty();
                }}
                placeholder="Helmut"
              />
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Engine & Tier */}
      <Card>
        <CardHeader>
          <CardTitle>Engine &amp; Budget Tier</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="space-y-2">
            <Label>Engine</Label>
            <div className="grid gap-3 sm:grid-cols-3">
              {(["cascaded", "s2s", "sentence"] as const).map((eng) => (
                <button
                  key={eng}
                  type="button"
                  onClick={() => {
                    setEngine(eng);
                    markDirty();
                  }}
                  className={`rounded-lg border p-3 text-left transition-colors ${
                    engine === eng
                      ? "border-primary bg-primary/10"
                      : "border-border hover:border-primary/50"
                  }`}
                >
                  <p className="font-medium capitalize">{eng === "s2s" ? "Speech-to-Speech" : eng}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {eng === "cascaded"
                      ? "STT → LLM → TTS — Best quality"
                      : eng === "s2s"
                        ? "Direct speech pipeline — Lowest latency"
                        : "Sentence cascade — Good balance"}
                  </p>
                </button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <Label>Budget Tier</Label>
            <div className="grid gap-3 sm:grid-cols-3">
              {(["fast", "standard", "deep"] as const).map((tier) => (
                <button
                  key={tier}
                  type="button"
                  onClick={() => {
                    setBudgetTier(tier);
                    markDirty();
                  }}
                  className={`rounded-lg border p-3 text-left transition-colors ${
                    budgetTier === tier
                      ? "border-primary bg-primary/10"
                      : "border-border hover:border-primary/50"
                  }`}
                >
                  <p className="font-medium capitalize">{tier}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {tier === "fast"
                      ? "Quick responses, basic reasoning"
                      : tier === "standard"
                        ? "Balanced quality and speed"
                        : "Thorough reasoning + tool use"}
                  </p>
                </button>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Knowledge */}
      <Card>
        <CardHeader>
          <CardTitle>Knowledge Scope</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap gap-2">
            {knowledgeScope.map((item, i) => (
              <Badge key={i} variant="secondary" className="gap-1 pr-1">
                {item}
                <button
                  type="button"
                  onClick={() => removeKnowledge(i)}
                  className="ml-1 rounded-full p-0.5 hover:bg-muted"
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))}
          </div>
          <div className="flex gap-2">
            <Input
              value={newKnowledge}
              onChange={(e) => setNewKnowledge(e.target.value)}
              placeholder="Add knowledge topic..."
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addKnowledge())}
            />
            <Button type="button" variant="outline" size="icon" onClick={addKnowledge}>
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Behavior Rules */}
      <Card>
        <CardHeader>
          <CardTitle>Behavior Rules</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {behaviorRules.length > 0 && (
            <ul className="space-y-2">
              {behaviorRules.map((rule, i) => (
                <li
                  key={i}
                  className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm"
                >
                  <span>{rule}</span>
                  <button
                    type="button"
                    onClick={() => removeRule(i)}
                    className="ml-2 text-muted-foreground hover:text-foreground"
                  >
                    <X className="h-4 w-4" />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className="flex gap-2">
            <Input
              value={newRule}
              onChange={(e) => setNewRule(e.target.value)}
              placeholder="Add behavior rule..."
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addRule())}
            />
            <Button type="button" variant="outline" size="icon" onClick={addRule}>
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Advanced */}
      <Card>
        <CardHeader>
          <CardTitle>Advanced</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <label className="flex items-center gap-3">
            <input
              type="checkbox"
              checked={addressOnly}
              onChange={(e) => {
                setAddressOnly(e.target.checked);
                markDirty();
              }}
              className="h-4 w-4 rounded border-border"
            />
            <div>
              <p className="text-sm font-medium">Address Only</p>
              <p className="text-xs text-muted-foreground">
                NPC only responds when directly addressed by name
              </p>
            </div>
          </label>
        </CardContent>
      </Card>

      {/* Delete */}
      {!isNew && (
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
                    Are you sure you want to delete &quot;{name}&quot;? This
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
      )}
    </div>
  );
}
