"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "./api";
import { hasMinRole } from "./rbac";
import type { Campaign, NPC, UserRole, UserPreferences, ProviderTestResult } from "./types";

// Auth
export function useUser() {
  return useQuery({
    queryKey: ["user"],
    queryFn: api.auth.me,
    retry: false,
    staleTime: 5 * 60 * 1000,
  });
}

/** Returns true if the current user has at least the given role. */
export function useHasRole(minRole: UserRole): boolean {
  const { data: user } = useUser();
  return hasMinRole(user?.role, minRole);
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

// Users
export function useUsers(params?: { role?: UserRole; limit?: number; offset?: number }) {
  return useQuery({
    queryKey: ["users", params],
    queryFn: () => api.users.list(params),
  });
}

export function useUpdateUser(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { display_name?: string; role?: UserRole }) =>
      api.users.update(id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      toast.success("User updated");
    },
    onError: (err) => toast.error("Failed to update user", { description: err.message }),
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.users.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      toast.success("User removed");
    },
    onError: (err) => toast.error("Failed to delete user", { description: err.message }),
  });
}

export function useCreateInvite() {
  return useMutation({
    mutationFn: (role: UserRole) => api.users.invite(role),
    onSuccess: () => {
      toast.success("Invite created");
    },
    onError: (err) => toast.error("Failed to create invite", { description: err.message }),
  });
}

export function useUpdateMe() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: { display_name: string }) => api.users.updateMe(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["user"] });
      toast.success("Profile updated");
    },
    onError: (err) => toast.error("Failed to update profile", { description: err.message }),
  });
}

export function useUpdatePreferences() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (prefs: Partial<UserPreferences>) => api.users.updatePreferences(prefs),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["user"] });
      toast.success("Preferences saved");
    },
    onError: (err) => toast.error("Failed to save preferences", { description: err.message }),
  });
}

// Audit Logs
export function useAuditLogs(params?: {
  limit?: number;
  offset?: number;
  resource_type?: string;
  action?: string;
}) {
  return useQuery({
    queryKey: ["audit-logs", params],
    queryFn: () => api.auditLogs.list(params),
  });
}

// Admin
export function useAdminStats() {
  return useQuery({
    queryKey: ["admin", "stats"],
    queryFn: api.admin.stats,
  });
}

export function useAdminUsers(params?: { limit?: number; offset?: number }) {
  return useQuery({
    queryKey: ["admin", "users", params],
    queryFn: () => api.admin.users(params),
  });
}

// Provider Testing
export function useTestProvider() {
  return useMutation({
    mutationFn: (data: {
      type: string;
      provider: string;
      api_key: string;
      base_url?: string;
    }) => api.providers.test(data),
    onSuccess: (result: ProviderTestResult) => {
      if (result.status === "ok") {
        toast.success(`${result.provider} connected`, {
          description: `Latency: ${result.latency_ms}ms`,
        });
      } else {
        toast.error(`${result.provider} failed`, {
          description: result.error,
        });
      }
    },
    onError: (err) => toast.error("Provider test failed", { description: err.message }),
  });
}

// Knowledge Graph
export function useKnowledgeGraph(campaignId: string) {
  return useQuery({
    queryKey: ["campaigns", campaignId, "knowledge", "graph"],
    queryFn: () => api.knowledge.graph(campaignId),
    enabled: !!campaignId,
  });
}

// Auth Providers
export function useAuthProviders() {
  return useQuery({
    queryKey: ["auth", "providers"],
    queryFn: api.auth.providers,
    staleTime: 60 * 60 * 1000, // Cache for 1 hour
    retry: false,
  });
}
