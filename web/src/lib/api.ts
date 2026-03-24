import type {
  Campaign,
  NPC,
  Session,
  TranscriptEntry,
  DashboardStats,
  ActivityItem,
  User,
  Invite,
  UserRole,
  UserPreferences,
  PaginatedResponse,
} from "./types";

const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "/api/v1";

class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const TOKEN_KEY = "glyphoxa_token";

export function getStoredToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(TOKEN_KEY);
}

export function setStoredToken(token: string): void {
  if (typeof window !== "undefined") {
    localStorage.setItem(TOKEN_KEY, token);
  }
}

export function clearStoredToken(): void {
  if (typeof window !== "undefined") {
    localStorage.removeItem(TOKEN_KEY);
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getStoredToken();
  const authHeaders: Record<string, string> = {};
  if (token) {
    authHeaders["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...authHeaders,
      ...options?.headers,
    },
  });

  if (res.status === 401) {
    clearStoredToken();
    if (typeof window !== "undefined") {
      window.location.href = "/login";
    }
    throw new ApiError(401, "Unauthorized");
  }

  if (!res.ok) {
    const body = await res.text();
    throw new ApiError(res.status, body || res.statusText);
  }

  if (res.status === 204) return undefined as T;
  return res.json();
}

// Unwraps the standard API envelope { data: T } returned by all endpoints.
async function requestData<T>(
  path: string,
  options?: RequestInit,
): Promise<T> {
  const envelope = await request<{ data: T }>(path, options);
  return envelope.data;
}

interface AuthResponse {
  data: {
    access_token: string;
    token_type: string;
    expires_in: number;
    user: User;
  };
}

// Auth
export const auth = {
  me: () => requestData<User>("/auth/me"),
  logout: () => {
    clearStoredToken();
    return request<void>("/auth/logout", { method: "POST" }).catch(() => {
      // Logout endpoint may not exist; clearing token is sufficient.
    });
  },
  discordUrl: () => `${API_BASE}/auth/discord`,
  loginApiKey: async (apiKey: string): Promise<AuthResponse> => {
    const res = await request<AuthResponse>("/auth/apikey", {
      method: "POST",
      body: JSON.stringify({ api_key: apiKey }),
    });
    setStoredToken(res.data.access_token);
    return res;
  },
};

// Dashboard
export const dashboard = {
  stats: () => requestData<DashboardStats>("/dashboard/stats"),
  activity: () => requestData<ActivityItem[]>("/dashboard/activity"),
  activeSessions: () => requestData<Session[]>("/dashboard/active-sessions"),
};

// Campaigns
export const campaigns = {
  list: () => requestData<Campaign[]>("/campaigns"),
  get: (id: string) => requestData<Campaign>(`/campaigns/${id}`),
  create: (data: Partial<Campaign>) =>
    requestData<Campaign>("/campaigns", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<Campaign>) =>
    requestData<Campaign>(`/campaigns/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/campaigns/${id}`, { method: "DELETE" }),
};

// NPCs
export const npcs = {
  list: (campaignId: string) =>
    requestData<NPC[]>(`/campaigns/${campaignId}/npcs`),
  get: (campaignId: string, npcId: string) =>
    requestData<NPC>(`/campaigns/${campaignId}/npcs/${npcId}`),
  create: (campaignId: string, data: Partial<NPC>) =>
    requestData<NPC>(`/campaigns/${campaignId}/npcs`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (campaignId: string, npcId: string, data: Partial<NPC>) =>
    requestData<NPC>(`/campaigns/${campaignId}/npcs/${npcId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (campaignId: string, npcId: string) =>
    request<void>(`/campaigns/${campaignId}/npcs/${npcId}`, {
      method: "DELETE",
    }),
};

// Sessions
export const sessions = {
  list: () => requestData<Session[]>("/sessions"),
  listByCampaign: (campaignId: string) =>
    requestData<Session[]>(`/campaigns/${campaignId}/sessions`),
  get: (id: string) => requestData<Session>(`/sessions/${id}`),
  stop: (id: string) =>
    request<void>(`/sessions/${id}`, { method: "DELETE" }),
  transcript: (id: string) =>
    requestData<TranscriptEntry[]>(`/sessions/${id}/transcript`),
};

// Users
export const users = {
  list: (params?: { role?: UserRole; limit?: number; offset?: number }) => {
    const q = new URLSearchParams();
    if (params?.role) q.set("role", params.role);
    if (params?.limit) q.set("limit", String(params.limit));
    if (params?.offset) q.set("offset", String(params.offset));
    const qs = q.toString();
    return request<PaginatedResponse<User>>(`/users${qs ? `?${qs}` : ""}`);
  },
  get: (id: string) => requestData<User>(`/users/${id}`),
  update: (id: string, data: { display_name?: string; role?: UserRole }) =>
    requestData<User>(`/users/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/users/${id}`, { method: "DELETE" }),
  invite: (role: UserRole = "viewer") =>
    requestData<Invite>("/users/invite", {
      method: "POST",
      body: JSON.stringify({ role }),
    }),
  updateMe: (data: { display_name: string }) =>
    requestData<User>("/auth/me", {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  updatePreferences: (prefs: Partial<UserPreferences>) =>
    requestData<User>("/auth/me/preferences", {
      method: "PATCH",
      body: JSON.stringify(prefs),
    }),
};

export const api = { auth, dashboard, campaigns, npcs, sessions, users };
