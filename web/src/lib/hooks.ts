"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "./api";
import type { Campaign, NPC, BotMode } from "./types";

// Auth
export function useUser() {
  return useQuery({
    queryKey: ["user"],
    queryFn: api.auth.me,
    retry: false,
    staleTime: 5 * 60 * 1000,
  });
}

// Dashboard
export function useDashboardStats() {
  return useQuery({
    queryKey: ["dashboard", "stats"],
    queryFn: api.dashboard.stats,
  });
}

export function useActiveSessions() {
  return useQuery({
    queryKey: ["dashboard", "active-sessions"],
    queryFn: api.dashboard.activeSessions,
    refetchInterval: 10_000,
  });
}

export function useActivity() {
  return useQuery({
    queryKey: ["dashboard", "activity"],
    queryFn: api.dashboard.activity,
  });
}

// Campaigns
export function useCampaigns() {
  return useQuery({
    queryKey: ["campaigns"],
    queryFn: api.campaigns.list,
  });
}

export function useCampaign(id: string) {
  return useQuery({
    queryKey: ["campaigns", id],
    queryFn: () => api.campaigns.get(id),
    enabled: !!id,
  });
}

export function useCreateCampaign() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<Campaign>) => api.campaigns.create(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns"] });
      toast.success("Campaign created");
    },
    onError: (err) => toast.error("Failed to create campaign", { description: err.message }),
  });
}

export function useUpdateCampaign(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<Campaign>) => api.campaigns.update(id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns"] });
      qc.invalidateQueries({ queryKey: ["campaigns", id] });
      toast.success("Campaign updated");
    },
    onError: (err) => toast.error("Failed to update campaign", { description: err.message }),
  });
}

export function useDeleteCampaign() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.campaigns.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns"] });
      toast.success("Campaign deleted");
    },
    onError: (err) => toast.error("Failed to delete campaign", { description: err.message }),
  });
}

// NPCs
export function useNPCs(campaignId: string) {
  return useQuery({
    queryKey: ["campaigns", campaignId, "npcs"],
    queryFn: () => api.npcs.list(campaignId),
    enabled: !!campaignId,
  });
}

export function useNPC(campaignId: string, npcId: string) {
  return useQuery({
    queryKey: ["campaigns", campaignId, "npcs", npcId],
    queryFn: () => api.npcs.get(campaignId, npcId),
    enabled: !!campaignId && !!npcId,
  });
}

export function useCreateNPC(campaignId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<NPC>) => api.npcs.create(campaignId, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns", campaignId, "npcs"] });
      toast.success("NPC created");
    },
    onError: (err) => toast.error("Failed to create NPC", { description: err.message }),
  });
}

export function useUpdateNPC(campaignId: string, npcId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<NPC>) =>
      api.npcs.update(campaignId, npcId, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns", campaignId, "npcs"] });
      qc.invalidateQueries({
        queryKey: ["campaigns", campaignId, "npcs", npcId],
      });
      toast.success("NPC updated");
    },
    onError: (err) => toast.error("Failed to update NPC", { description: err.message }),
  });
}

export function useDeleteNPC(campaignId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (npcId: string) => api.npcs.delete(campaignId, npcId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns", campaignId, "npcs"] });
      toast.success("NPC deleted");
    },
    onError: (err) => toast.error("Failed to delete NPC", { description: err.message }),
  });
}

// Sessions
export function useSessions() {
  return useQuery({
    queryKey: ["sessions"],
    queryFn: api.sessions.list,
  });
}

export function useSession(id: string) {
  return useQuery({
    queryKey: ["sessions", id],
    queryFn: () => api.sessions.get(id),
    enabled: !!id,
  });
}

export function useTranscript(sessionId: string, live?: boolean) {
  return useQuery({
    queryKey: ["sessions", sessionId, "transcript"],
    queryFn: () => api.sessions.transcript(sessionId),
    enabled: !!sessionId,
    refetchInterval: live ? 5_000 : false,
  });
}

export function useStopSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.sessions.stop(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["sessions"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
      toast.success("Session stopped");
    },
    onError: (err) => toast.error("Failed to stop session", { description: err.message }),
  });
}

// Onboarding
export function useDiscordGuilds() {
  return useQuery({
    queryKey: ["onboarding", "guilds"],
    queryFn: api.onboarding.guilds,
    retry: false,
    staleTime: 60 * 1000,
  });
}

export function useOnboardingSetup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      botMode,
      guildIds,
      botToken,
    }: {
      botMode: BotMode;
      guildIds: string[];
      botToken?: string;
    }) => api.onboarding.setup(botMode, guildIds, botToken),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["user"] });
      toast.success("Setup complete! Welcome to Glyphoxa.");
    },
    onError: (err) => toast.error("Setup failed", { description: err.message }),
  });
}
