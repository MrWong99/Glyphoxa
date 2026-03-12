package main

import (
	"context"
	"sync"

	"github.com/MrWong99/zhi/pkg/zhiplugin/config"
)

// valueDefs contains all configuration values for the Glyphoxa deployment workspace.
var valueDefs = func() []ValueDef {
	defs := []ValueDef{
		// ── core ──────────────────────────────────────────────────────────────
		{
			Path: "core/release-name", Default: "glyphoxa",
			Section: "Core", DisplayName: "Release Name",
			Description: "Resource naming prefix for all K8s resources; RFC 1123 DNS label",
			Type:        "string",
		},
		{
			Path: "core/namespace", Default: "glyphoxa",
			Section: "Core", DisplayName: "Namespace",
			Description: "Kubernetes namespace for deployment; RFC 1123 DNS label",
			Type:        "string",
		},
		{
			Path: "core/topology", Default: "shared",
			Section: "Core", DisplayName: "Topology",
			Description: "Deployment topology: shared (multi-tenant) or dedicated (single-tenant)",
			Type:        "string", SelectFrom: []string{"shared", "dedicated"},
		},
		{
			Path: "core/image-repository", Default: "ghcr.io/mrwong99/glyphoxa",
			Section: "Core", DisplayName: "Image Repository",
			Description: "Container image repository",
			Type:        "string",
		},
		{
			Path: "core/image-tag", Default: "latest",
			Section: "Core", DisplayName: "Image Tag",
			Description: "Container image tag (defaults to release workflow's latest tag)",
			Type:        "string",
		},
		{
			Path: "core/image-pull-policy", Default: "IfNotPresent",
			Section: "Core", DisplayName: "Image Pull Policy",
			Description: "Kubernetes image pull policy",
			Type:        "string", SelectFrom: []string{"Always", "IfNotPresent", "Never"},
		},
		{
			Path: "core/image-pull-secrets", Default: "",
			Section: "Core", DisplayName: "Image Pull Secrets",
			Description: "Comma-separated list of K8s secret names for pulling images",
			Type:        "string",
		},
		{
			Path: "core/run-as-user", Default: 999,
			Section: "Core", DisplayName: "Run As User",
			Description: "UID for the container security context (runAsUser)",
			Type:        "int",
		},
		{
			Path: "core/run-as-group", Default: 999,
			Section: "Core", DisplayName: "Run As Group",
			Description: "GID for the container security context (runAsGroup)",
			Type:        "int",
		},
		{
			Path: "core/fs-group", Default: 999,
			Section: "Core", DisplayName: "FS Group",
			Description: "GID for volume ownership (fsGroup)",
			Type:        "int",
		},

		// ── gateway ──────────────────────────────────────────────────────────
		{
			Path: "gateway/replica-count", Default: 3,
			Section: "Gateway", DisplayName: "Replica Count",
			Description: "Number of gateway pod replicas",
			Type:        "int",
		},
		{
			Path: "gateway/admin-key", Default: "",
			Section: "Gateway", DisplayName: "Admin Key",
			Description: "Authentication key for the admin API",
			Type:        "string", Password: true,
		},
		{
			Path: "gateway/requests/cpu", Default: "250m",
			Section: "Gateway Resources", DisplayName: "CPU Request",
			Description: "CPU request for gateway pods",
			Type:        "string",
		},
		{
			Path: "gateway/requests/memory", Default: "256Mi",
			Section: "Gateway Resources", DisplayName: "Memory Request",
			Description: "Memory request for gateway pods",
			Type:        "string",
		},
		{
			Path: "gateway/limits/cpu", Default: "1",
			Section: "Gateway Resources", DisplayName: "CPU Limit",
			Description: "CPU limit for gateway pods",
			Type:        "string",
		},
		{
			Path: "gateway/limits/memory", Default: "512Mi",
			Section: "Gateway Resources", DisplayName: "Memory Limit",
			Description: "Memory limit for gateway pods",
			Type:        "string",
		},
		{
			Path: "gateway/node-selector", Default: "",
			Section: "Gateway Scheduling", DisplayName: "Node Selector",
			Description: "Node selector key-value pairs as YAML (e.g., 'tier: shared')",
			Type:        "string", Placeholder: "tier: shared",
		},
		{
			Path: "gateway/tolerations", Default: "",
			Section: "Gateway Scheduling", DisplayName: "Tolerations",
			Description: "Kubernetes tolerations as YAML",
			Type:        "string",
		},
		{
			Path: "gateway/pod-anti-affinity", Default: true,
			Section: "Gateway Scheduling", DisplayName: "Pod Anti-Affinity",
			Description: "Prefer scheduling gateway pods on different nodes",
			Type:        "bool",
		},
		{
			Path: "gateway/service-account-create", Default: true,
			Section: "Gateway", DisplayName: "Create Service Account",
			Description: "Create a dedicated ServiceAccount with RBAC for job management",
			Type:        "bool",
		},
		{
			Path: "gateway/service-account-name", Default: "glyphoxa-gateway",
			Section: "Gateway", DisplayName: "Service Account Name",
			Description: "Name of the gateway ServiceAccount",
			Type:        "string",
		},
		{
			Path: "gateway/service-account-annotations", Default: "",
			Section: "Gateway", DisplayName: "Service Account Annotations",
			Description: "Annotations for the ServiceAccount as YAML (e.g., IRSA annotations)",
			Type:        "string",
		},

		// ── worker ───────────────────────────────────────────────────────────
		{
			Path: "worker/resource-profile", Default: "cloud",
			Section: "Worker", DisplayName: "Resource Profile",
			Description: "Worker resource profile determining CPU/memory allocation",
			Type:        "string", SelectFrom: []string{"cloud", "whisper-native", "local-llm"},
		},
		{
			Path: "worker/node-selector", Default: "",
			Section: "Worker Scheduling", DisplayName: "Node Selector",
			Description: "Node selector key-value pairs as YAML",
			Type:        "string",
		},
		{
			Path: "worker/tolerations", Default: "",
			Section: "Worker Scheduling", DisplayName: "Tolerations",
			Description: "Kubernetes tolerations as YAML",
			Type:        "string",
		},
		{
			Path: "worker/gpu-enabled", Default: true,
			Section: "Worker", DisplayName: "GPU Scheduling",
			Description: "Enable GPU node scheduling for local-llm profile",
			Type:        "bool",
		},

		// ── mcp-gateway ──────────────────────────────────────────────────────
		{
			Path: "mcp-gateway/replica-count", Default: 2,
			Section: "MCP Gateway", DisplayName: "Replica Count",
			Description: "Number of MCP gateway pod replicas",
			Type:        "int",
		},
		{
			Path: "mcp-gateway/requests/cpu", Default: "250m",
			Section: "MCP Gateway Resources", DisplayName: "CPU Request",
			Description: "CPU request for MCP gateway pods",
			Type:        "string",
		},
		{
			Path: "mcp-gateway/requests/memory", Default: "256Mi",
			Section: "MCP Gateway Resources", DisplayName: "Memory Request",
			Description: "Memory request for MCP gateway pods",
			Type:        "string",
		},
		{
			Path: "mcp-gateway/limits/cpu", Default: "1",
			Section: "MCP Gateway Resources", DisplayName: "CPU Limit",
			Description: "CPU limit for MCP gateway pods",
			Type:        "string",
		},
		{
			Path: "mcp-gateway/limits/memory", Default: "512Mi",
			Section: "MCP Gateway Resources", DisplayName: "Memory Limit",
			Description: "Memory limit for MCP gateway pods",
			Type:        "string",
		},
		{
			Path: "mcp-gateway/node-selector", Default: "",
			Section: "MCP Gateway Scheduling", DisplayName: "Node Selector",
			Description: "Node selector key-value pairs as YAML",
			Type:        "string",
		},
		{
			Path: "mcp-gateway/tolerations", Default: "",
			Section: "MCP Gateway Scheduling", DisplayName: "Tolerations",
			Description: "Kubernetes tolerations as YAML",
			Type:        "string",
		},

		// ── database ─────────────────────────────────────────────────────────
		{
			Path: "database/dsn", Default: "",
			Section: "Database", DisplayName: "Database DSN",
			Description: "PostgreSQL connection string (used when postgresql component is disabled)",
			Type:        "string", Password: true,
		},

		// ── autoscaling ──────────────────────────────────────────────────────
		{
			Path: "autoscaling/min-replicas", Default: 2,
			Section: "Autoscaling", DisplayName: "Min Replicas",
			Description: "Minimum number of gateway replicas",
			Type:        "int",
		},
		{
			Path: "autoscaling/max-replicas", Default: 10,
			Section: "Autoscaling", DisplayName: "Max Replicas",
			Description: "Maximum number of gateway replicas",
			Type:        "int",
		},
		{
			Path: "autoscaling/target-cpu", Default: 70,
			Section: "Autoscaling", DisplayName: "Target CPU %",
			Description: "Target CPU utilization percentage for autoscaling",
			Type:        "int",
		},

		// ── postgresql ───────────────────────────────────────────────────────
		{
			Path: "postgresql/postgres-password", Default: "",
			Section: "PostgreSQL", DisplayName: "Postgres Password",
			Description: "Password for the postgres superuser",
			Type:        "string", Password: true, Required: true,
		},
		{
			Path: "postgresql/database", Default: "glyphoxa",
			Section: "PostgreSQL", DisplayName: "Database Name",
			Description: "Name of the database to create",
			Type:        "string",
		},
		{
			Path: "postgresql/persistence-size", Default: "10Gi",
			Section: "PostgreSQL", DisplayName: "Persistence Size",
			Description: "Size of the PostgreSQL persistent volume",
			Type:        "string",
		},

		// ── config/server ────────────────────────────────────────────────────
		{
			Path: "config/server/log-level", Default: "info",
			Section: "Application — Server", DisplayName: "Log Level",
			Description: "Application log level",
			Type:        "string", SelectFrom: []string{"debug", "info", "warn", "error"},
		},

		// ── config/memory ────────────────────────────────────────────────────
		{
			Path: "config/memory/embedding-dimensions", Default: 1536,
			Section: "Application — Memory", DisplayName: "Embedding Dimensions",
			Description: "Vector embedding dimensions (must match embeddings provider model)",
			Type:        "int",
		},

		// ── config/campaign ──────────────────────────────────────────────────
		{
			Path: "config/campaign/name", Default: "",
			Section: "Application — Campaign", DisplayName: "Campaign Name",
			Description: "Campaign display name",
			Type:        "string",
		},
		{
			Path: "config/campaign/system", Default: "",
			Section: "Application — Campaign", DisplayName: "Game System",
			Description: "Game system identifier (e.g., dnd5e, pf2e)",
			Type:        "string",
		},

		// ── config/transcript ────────────────────────────────────────────────
		{
			Path: "config/transcript/llm-correction", Default: false,
			Section: "Application — Transcript", DisplayName: "LLM Correction",
			Description: "Enable LLM-based transcript correction",
			Type:        "bool",
		},

		// ── config/mcp-servers ───────────────────────────────────────────────
		{
			Path: "config/mcp-servers", Default: "",
			Section: "Application — MCP", DisplayName: "MCP Servers",
			Description: "MCP server definitions as YAML",
			Type:        "string", Placeholder: "- name: dice\n  transport: stdio\n  command: /usr/local/bin/mcp-dice",
		},
	}

	// ── config/providers ─────────────────────────────────────────────────
	providers := []struct {
		prefix, section, display string
	}{
		{"config/providers/llm", "Providers — LLM", "LLM"},
		{"config/providers/stt", "Providers — STT", "STT"},
		{"config/providers/tts", "Providers — TTS", "TTS"},
		{"config/providers/s2s", "Providers — S2S", "S2S"},
		{"config/providers/embeddings", "Providers — Embeddings", "Embeddings"},
		{"config/providers/vad", "Providers — VAD", "VAD"},
		{"config/providers/audio", "Providers — Audio", "Audio"},
	}
	for _, p := range providers {
		defs = append(defs, providerDefs(p.prefix, p.section, p.display)...)
	}

	return defs
}()

// glyphoxaPlugin implements config.Plugin.
type glyphoxaPlugin struct {
	mu     sync.RWMutex
	paths  []string
	values map[string]*config.Value
}

func newGlyphoxaPlugin() *glyphoxaPlugin {
	p := &glyphoxaPlugin{
		values: make(map[string]*config.Value, len(valueDefs)),
		paths:  make([]string, 0, len(valueDefs)),
	}
	for _, v := range valueDefs {
		p.paths = append(p.paths, v.Path)
		p.values[v.Path] = v.ToValue()
	}
	return p
}

func (p *glyphoxaPlugin) List(_ context.Context) ([]string, error) {
	return p.paths, nil
}

func (p *glyphoxaPlugin) Get(_ context.Context, path string) (config.Value, bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.values[path]
	if !ok {
		return config.Value{}, false, nil
	}
	return *v, true, nil
}

func (p *glyphoxaPlugin) Set(_ context.Context, path string, v config.Value) error {
	if err := config.ValidatePath(path); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[path] = &v
	return nil
}

func (p *glyphoxaPlugin) Validate(_ context.Context, path string, tree config.TreeReader) ([]config.ValidationResult, error) {
	if fn, ok := validators[path]; ok {
		v, found := tree.Get(path)
		if !found {
			return nil, nil
		}
		return fn(v, tree)
	}
	return nil, nil
}
