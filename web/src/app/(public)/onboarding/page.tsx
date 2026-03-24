"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sparkles, Loader2 } from "lucide-react";
import { api } from "@/lib/api";

type Step = "welcome" | "tenant" | "done";

export default function OnboardingPage() {
  const router = useRouter();
  const [step, setStep] = useState<Step>("welcome");
  const [tenantId, setTenantId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [licenseTier, setLicenseTier] = useState("shared");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleCreateTenant(e: React.FormEvent) {
    e.preventDefault();
    if (!tenantId.trim()) return;

    setError("");
    setLoading(true);
    try {
      await api.onboarding.complete({
        tenant_id: tenantId.trim().toLowerCase().replace(/\s+/g, "-"),
        display_name: displayName.trim() || tenantId.trim(),
        license_tier: licenseTier,
      });
      setStep("done");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create workspace");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <div className="pointer-events-none fixed inset-0 overflow-hidden">
        <div className="absolute -top-1/2 left-1/2 h-[800px] w-[800px] -translate-x-1/2 rounded-full bg-primary/5 blur-3xl" />
      </div>

      <div className="relative w-full max-w-lg space-y-6">
        {step === "welcome" && (
          <Card className="border-border/50 shadow-xl shadow-primary/5">
            <CardHeader className="text-center">
              <div className="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10">
                <Sparkles className="h-7 w-7 text-primary" />
              </div>
              <CardTitle className="text-2xl">Welcome to Glyphoxa</CardTitle>
              <CardDescription>
                Let&apos;s set up your workspace so you can start creating AI voice NPCs for your tabletop sessions.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button className="w-full" size="lg" onClick={() => setStep("tenant")}>
                Get Started
              </Button>
            </CardContent>
          </Card>
        )}

        {step === "tenant" && (
          <Card className="border-border/50 shadow-xl shadow-primary/5">
            <CardHeader>
              <CardTitle>Create Your Workspace</CardTitle>
              <CardDescription>
                Choose a unique identifier and display name for your workspace.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form onSubmit={handleCreateTenant} className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="tenant-id">Workspace ID</Label>
                  <Input
                    id="tenant-id"
                    placeholder="my-guild"
                    value={tenantId}
                    onChange={(e) => setTenantId(e.target.value)}
                    pattern="[a-zA-Z0-9][a-zA-Z0-9_-]*"
                    title="Letters, numbers, hyphens, and underscores"
                    required
                  />
                  <p className="text-xs text-muted-foreground">
                    Used internally. Letters, numbers, hyphens, and underscores only.
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="display-name">Display Name</Label>
                  <Input
                    id="display-name"
                    placeholder="My Gaming Group"
                    value={displayName}
                    onChange={(e) => setDisplayName(e.target.value)}
                  />
                </div>

                <div className="space-y-2">
                  <Label>Plan</Label>
                  <div className="grid grid-cols-2 gap-3">
                    <button
                      type="button"
                      className={`rounded-lg border p-3 text-left text-sm transition-colors ${
                        licenseTier === "shared"
                          ? "border-primary bg-primary/5"
                          : "border-border hover:border-primary/50"
                      }`}
                      onClick={() => setLicenseTier("shared")}
                    >
                      <div className="font-medium">Shared</div>
                      <div className="text-muted-foreground">SaaS managed bot</div>
                    </button>
                    <button
                      type="button"
                      className={`rounded-lg border p-3 text-left text-sm transition-colors ${
                        licenseTier === "guild"
                          ? "border-primary bg-primary/5"
                          : "border-border hover:border-primary/50"
                      }`}
                      onClick={() => setLicenseTier("guild")}
                    >
                      <div className="font-medium">Guild</div>
                      <div className="text-muted-foreground">Bring your own bot</div>
                    </button>
                  </div>
                </div>

                {error && (
                  <p className="text-sm text-destructive" role="alert">{error}</p>
                )}

                <Button type="submit" className="w-full" size="lg" disabled={loading || !tenantId.trim()}>
                  {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                  Create Workspace
                </Button>
              </form>
            </CardContent>
          </Card>
        )}

        {step === "done" && (
          <Card className="border-border/50 shadow-xl shadow-primary/5">
            <CardHeader className="text-center">
              <div className="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-green-500/10">
                <Sparkles className="h-7 w-7 text-green-500" />
              </div>
              <CardTitle className="text-2xl">You&apos;re All Set!</CardTitle>
              <CardDescription>
                Your workspace has been created. Head to the dashboard to create your first campaign.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button className="w-full" size="lg" onClick={() => router.push("/dashboard")}>
                Go to Dashboard
              </Button>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
