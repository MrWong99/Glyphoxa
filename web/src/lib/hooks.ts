"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import type { Campaign, NPC } from "./types";

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
    onSuccess: () => qc.invalidateQueries({ queryKey: ["campaigns"] }),
  });
}

export function useUpdateCampaign(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<Campaign>) => api.campaigns.update(id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["campaigns"] });
      qc.invalidateQueries({ queryKey: ["campaigns", id] });
    },
  });
}

export function useDeleteCampaign() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.campaigns.delete(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["campaigns"] }),
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
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["campaigns", campaignId, "npcs"] }),
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
    },
  });
}

export function useDeleteNPC(campaignId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (npcId: string) => api.npcs.delete(campaignId, npcId),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["campaigns", campaignId, "npcs"] }),
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

export function useTranscript(sessionId: string) {
  return useQuery({
    queryKey: ["sessions", sessionId, "transcript"],
    queryFn: () => api.sessions.transcript(sessionId),
    enabled: !!sessionId,
  });
}

export function useStopSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.sessions.stop(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["sessions"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}
