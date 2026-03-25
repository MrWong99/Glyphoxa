// API types for the Glyphoxa Web Management Service

export type UserRole = "super_admin" | "tenant_admin" | "dm" | "viewer";

export interface UserPreferences {
  theme?: "light" | "dark" | "system";
  locale?: string;
  [key: string]: unknown;
}

export interface User {
  id: string;
  email: string;
  display_name: string;
  avatar_url: string | null;
  role: UserRole;
  preferences?: UserPreferences;
  tenant_id: string;
  created_at: string;
  updated_at?: string;
}

export interface Invite {
  id: string;
  tenant_id: string;
  role: UserRole;
  created_by: string;
  token: string;
  expires_at: string;
  used_by?: string;
  used_at?: string;
  created_at: string;
}

export interface PaginatedResponse<T> {
  data: T[];
  total: number;
}

export interface Campaign {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  game_system: string;
  language: string;
  npc_count: number;
  last_session_at: string | null;
  has_active_session: boolean;
  settings: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface NPC {
  id: string;
  campaign_id: string;
  name: string;
  personality: string;
  voice_provider: string;
  voice_id: string;
  voice?: {
    provider: string;
    voice_id: string;
    pitch_shift?: number;
    speed?: number;
  };
  voice_config: {
    pitch?: number;
    speed?: number;
  };
  engine: "cascaded" | "s2s" | "sentence";
  budget_tier: "fast" | "standard" | "deep";
  knowledge_scope: string[];
  secret_knowledge: string[];
  behavior_rules: string[];
  address_only: boolean;
  gm_helper: boolean;
  custom_attributes: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface Session {
  id: string;
  campaign_id: string;
  campaign_name: string;
  guild_name: string;
  status: "active" | "ended" | "error";
  started_at: string;
  ended_at: string | null;
  duration_seconds: number;
  npc_names: string[];
}

export interface TranscriptEntry {
  id: string;
  session_id: string;
  speaker: string;
  speaker_type: "player" | "npc" | "system";
  content: string;
  timestamp: string;
}

export interface ActivityItem {
  id: string;
  type: "session_ended" | "session_started" | "npc_created" | "npc_updated" | "campaign_created";
  description: string;
  timestamp: string;
  campaign_id?: string;
}

export interface DashboardStats {
  campaign_count: number;
  active_session_count: number;
  hours_used: number;
  hours_limit: number;
}

export interface AuditLogEntry {
  id: number;
  tenant_id: string | null;
  user_id: string | null;
  action: string;
  resource_type: string;
  resource_id: string;
  changes: Record<string, unknown> | null;
  ip_address: string | null;
  user_agent: string | null;
  created_at: string;
}

export interface AdminDashboardStats {
  total_tenants: number;
  total_users: number;
  total_campaigns: number;
  active_sessions: number;
  total_session_hours: number;
  audit_log_count: number;
}

export interface ProviderTestResult {
  type: string;
  provider: string;
  status: "ok" | "error";
  latency_ms: number;
  error?: string;
}

export interface KnowledgeEntity {
  id: string;
  campaign_id: string;
  type: string;
  name: string;
  attributes: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface KnowledgeRelationship {
  source_id: string;
  target_id: string;
  source_name: string;
  target_name: string;
  rel_type: string;
  attributes?: Record<string, unknown>;
}

export interface KnowledgeGraphData {
  entities: KnowledgeEntity[];
  relationships: KnowledgeRelationship[];
}

export interface AuthProviders {
  discord: boolean;
  google: boolean;
  github: boolean;
  apikey: boolean;
}

export type GameSystem =
  | "D&D 5e"
  | "D&D 5e (2024)"
  | "Pathfinder 2e"
  | "Das Schwarze Auge"
  | "Call of Cthulhu"
  | "Shadowrun"
  | "Fate Core"
  | "Savage Worlds"
  | "Other";
