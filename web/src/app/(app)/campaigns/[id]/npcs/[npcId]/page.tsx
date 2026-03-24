"use client";

import { useState, use } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Save, Trash2, Plus, X, Volume2, ImagePlus, Wand2 } from "lucide-react";
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
import { Breadcrumbs } from "@/components/breadcrumbs";
import {
  useNPC,
  useCampaign,
  useUpdateNPC,
  useDeleteNPC,
} from "@/lib/hooks";
import { toast } from "sonner";
import type { NPC } from "@/lib/types";

const personalityTemplates = [
  { label: "Gruff Guard", text: "A stern but fair guard who has watched over this area for decades. Suspicious of strangers, protective of the townsfolk, speaks in short, clipped sentences." },
  { label: "Cheerful Innkeeper", text: "A warm and welcoming innkeeper who loves nothing more than a good story and a full tavern. Knows all the local gossip and is always ready with advice." },
  { label: "Mysterious Merchant", text: "A traveling merchant who deals in rare and unusual goods. Speaks cryptically, knows more than they let on, and always has the right item for the right price." },
  { label: "Wise Elder", text: "An ancient scholar or village elder with deep knowledge of history and lore. Patient and thoughtful, speaks in measured tones, occasionally shares prophecies." },
];

interface NPCFormProps {
  npc: NPC;
  campaignId: string;
  campaignName: string;
}

function NPCForm({ npc, campaignId, campaignName }: NPCFormProps) {
  const router = useRouter();
  const updateNPC = useUpdateNPC(campaignId, npc.id);
  const deleteNPC = useDeleteNPC(campaignId);

  const [name, setName] = useState(npc.name);
  const [personality, setPersonality] = useState(npc.personality);
  const [voiceProvider, setVoiceProvider] = useState(npc.voice_provider);
  const [voiceId, setVoiceId] = useState(npc.voice_id);
  const [engine, setEngine] = useState<"cascaded" | "s2s" | "sentence">(npc.engine);
  const [budgetTier, setBudgetTier] = useState<"fast" | "standard" | "deep">(npc.budget_tier);
  const [knowledgeScope, setKnowledgeScope] = useState<string[]>(npc.knowledge_scope ?? []);
  const [behaviorRules, setBehaviorRules] = useState<string[]>(npc.behavior_rules ?? []);
  const [addressOnly, setAddressOnly] = useState(npc.address_only);
  const [newKnowledge, setNewKnowledge] = useState("");
  const [newRule, setNewRule] = useState("");
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "NPC name is required";
  if (!voiceId.trim()) errors.voiceId = "Voice ID is required";
  if (personality.length > 2000) errors.personality = "Personality must be under 2000 characters";

  function touch(field: string) {
    setTouched((prev) => ({ ...prev, [field]: true }));
  }

  async function handleSave() {
    setTouched({ name: true, voiceId: true, personality: true });
    if (Object.keys(errors).length > 0) return;

    await updateNPC.mutateAsync({
      name: name.trim(),
      personality: personality.trim(),
      voice_provider: voiceProvider,
      voice_id: voiceId.trim(),
      engine,
      budget_tier: budgetTier,
      knowledge_scope: knowledgeScope,
      behavior_rules: behaviorRules,
      address_only: addressOnly,
    });
  }

  async function handleDelete() {
    await deleteNPC.mutateAsync(npc.id);
    router.push(`/campaigns/${campaignId}`);
  }

  function addKnowledge() {
    if (newKnowledge.trim()) {
      setKnowledgeScope([...knowledgeScope, newKnowledge.trim()]);
      setNewKnowledge("");
    }
  }

  function addRule() {
    if (newRule.trim()) {
      setBehaviorRules([...behaviorRules, newRule.trim()]);
      setNewRule("");
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Campaigns", href: "/campaigns" },
          { label: campaignName, href: `/campaigns/${campaignId}` },
          { label: name || "NPC" },
        ]}
      />

      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">
          {name || "NPC"}
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

      {/* Identity with Avatar */}
      <Card>
        <CardHeader><CardTitle>Identity</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="flex gap-4">
            <div className="flex h-20 w-20 shrink-0 items-center justify-center rounded-xl border-2 border-dashed border-border bg-muted/30 transition-colors hover:border-primary/30 hover:bg-primary/5 cursor-pointer" role="button" aria-label="Upload character portrait" tabIndex={0}>
              <ImagePlus className="h-6 w-6 text-muted-foreground/50" />
            </div>
            <div className="flex-1 space-y-2">
              <Label htmlFor="npc-name">Name</Label>
              <Input
                id="npc-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onBlur={() => touch("name")}
                placeholder="Heinrich der W&auml;chter"
                aria-invalid={touched.name && !!errors.name}
              />
              {touched.name && errors.name && (
                <p className="text-xs text-destructive">{errors.name}</p>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Personality with Templates */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle>Personality</CardTitle>
            <div className="flex flex-wrap gap-1.5">
              {personalityTemplates.map((t) => (
                <Button
                  key={t.label}
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 text-xs"
                  onClick={() => {
                    setPersonality(t.text);
                    toast.info(`Template "${t.label}" applied`);
                  }}
                >
                  <Wand2 className="mr-1 h-3 w-3" />
                  {t.label}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-2">
          <Textarea
            value={personality}
            onChange={(e) => setPersonality(e.target.value)}
            onBlur={() => touch("personality")}
            placeholder="A stern but fair city guard who has watched the gates of Rabenheim for over 20 years. He knows every resident by name and is suspicious of strangers..."
            rows={6}
            aria-invalid={touched.personality && !!errors.personality}
          />
          <div className="flex items-center justify-between">
            {touched.personality && errors.personality ? (
              <p className="text-xs text-destructive">{errors.personality}</p>
            ) : (
              <span />
            )}
            <p className="text-xs text-muted-foreground">
              {personality.length} / 2000 characters
            </p>
          </div>
        </CardContent>
      </Card>

      {/* Voice with Preview */}
      <Card>
        <CardHeader><CardTitle>Voice</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select
                value={voiceProvider}
                onValueChange={(v) => { if (v) setVoiceProvider(v); }}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="elevenlabs">ElevenLabs</SelectItem>
                  <SelectItem value="azure">Azure</SelectItem>
                  <SelectItem value="google">Google</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="voice-id">Voice ID</Label>
              <div className="flex gap-2">
                <Input
                  id="voice-id"
                  value={voiceId}
                  onChange={(e) => setVoiceId(e.target.value)}
                  onBlur={() => touch("voiceId")}
                  placeholder="Helmut"
                  aria-invalid={touched.voiceId && !!errors.voiceId}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  disabled={!voiceId.trim()}
                  aria-label="Preview voice"
                  onClick={() => toast.info("Voice preview coming soon")}
                >
                  <Volume2 className="h-4 w-4" />
                </Button>
              </div>
              {touched.voiceId && errors.voiceId && (
                <p className="text-xs text-destructive">{errors.voiceId}</p>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Engine & Tier */}
      <Card>
        <CardHeader><CardTitle>Engine &amp; Budget Tier</CardTitle></CardHeader>
        <CardContent className="space-y-6">
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Engine</legend>
            <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Engine selection">
              {(["cascaded", "s2s", "sentence"] as const).map((eng) => (
                <button
                  key={eng}
                  type="button"
                  role="radio"
                  aria-checked={engine === eng}
                  onClick={() => setEngine(eng)}
                  className={`rounded-lg border p-3 text-left transition-all duration-200 ${
                    engine === eng
                      ? "border-primary bg-primary/10 shadow-sm shadow-primary/10"
                      : "border-border hover:border-primary/50 hover:bg-primary/5"
                  }`}
                >
                  <p className="font-medium capitalize">{eng === "s2s" ? "Speech-to-Speech" : eng}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {eng === "cascaded"
                      ? "STT \u2192 LLM \u2192 TTS \u2014 Best quality"
                      : eng === "s2s"
                        ? "Direct speech pipeline \u2014 Lowest latency"
                        : "Sentence cascade \u2014 Good balance"}
                  </p>
                </button>
              ))}
            </div>
          </fieldset>

          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Budget Tier</legend>
            <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Budget tier selection">
              {(["fast", "standard", "deep"] as const).map((tier) => (
                <button
                  key={tier}
                  type="button"
                  role="radio"
                  aria-checked={budgetTier === tier}
                  onClick={() => setBudgetTier(tier)}
                  className={`rounded-lg border p-3 text-left transition-all duration-200 ${
                    budgetTier === tier
                      ? "border-primary bg-primary/10 shadow-sm shadow-primary/10"
                      : "border-border hover:border-primary/50 hover:bg-primary/5"
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
          </fieldset>
        </CardContent>
      </Card>

      {/* Knowledge */}
      <Card>
        <CardHeader><CardTitle>Knowledge Scope</CardTitle></CardHeader>
        <CardContent className="space-y-3">
          {knowledgeScope.length === 0 && (
            <p className="text-sm text-muted-foreground">
              Add topics this NPC has knowledge about (e.g., &quot;local tavern history&quot;, &quot;guard patrol routes&quot;).
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {knowledgeScope.map((item, i) => (
              <Badge key={i} variant="secondary" className="gap-1 pr-1">
                {item}
                <button
                  type="button"
                  aria-label={`Remove ${item}`}
                  onClick={() => setKnowledgeScope(knowledgeScope.filter((_, j) => j !== i))}
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
            <Button type="button" variant="outline" size="icon" aria-label="Add knowledge topic" onClick={addKnowledge}>
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Behavior Rules */}
      <Card>
        <CardHeader><CardTitle>Behavior Rules</CardTitle></CardHeader>
        <CardContent className="space-y-3">
          {behaviorRules.length === 0 && (
            <p className="text-sm text-muted-foreground">
              Define rules that govern this NPC&apos;s behavior (e.g., &quot;Never reveal the secret passage&quot;).
            </p>
          )}
          {behaviorRules.length > 0 && (
            <ul className="space-y-2">
              {behaviorRules.map((rule, i) => (
                <li
                  key={i}
                  className="flex items-center justify-between rounded-lg border border-border px-3 py-2.5 text-sm transition-colors hover:bg-muted/30"
                >
                  <span className="flex items-center gap-2">
                    <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-medium text-primary">{i + 1}</span>
                    {rule}
                  </span>
                  <button
                    type="button"
                    aria-label={`Remove rule: ${rule}`}
                    onClick={() => setBehaviorRules(behaviorRules.filter((_, j) => j !== i))}
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
            <Button type="button" variant="outline" size="icon" aria-label="Add behavior rule" onClick={addRule}>
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Advanced */}
      <Card>
        <CardHeader><CardTitle>Advanced</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <label className="flex cursor-pointer items-center gap-3 rounded-lg p-2 transition-colors hover:bg-muted/30">
            <input
              type="checkbox"
              checked={addressOnly}
              onChange={(e) => setAddressOnly(e.target.checked)}
              className="h-4 w-4 rounded border-border accent-primary"
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
