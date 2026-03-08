---
title: "feat: Add zhi-based deployment alternative"
type: feat
status: active
date: 2026-03-08
---

## Enhancement Summary

**Deepened on:** 2026-03-08
**Reviewed on:** 2026-03-08
**Sections enhanced:** 5 phases + architecture + acceptance criteria
**Research agents used:** Architecture Strategist, Security Sentinel, Performance Oracle, Deployment Verification, Code Simplicity, Pattern Consistency, Best Practices

### Key Improvements

1. **Security hardening**: RFC 1123 validators for namespace/release-name (prevent shell injection), PostgreSQL password via `--values` temp file (not `--set`), secret creation via `kubectl create secret`, trap cleanup deletes script files containing secrets after apply, full restricted pod security context on all specs
2. **ConfigMap-triggered restarts**: `checksum/config` pod annotation triggers rolling updates when ConfigMap changes — replaces fragile hash-based bash logic
3. **Simplified component model**: Collapse 5 mandatory components into 1 (`core`), hardcode stable values (ports, deadlines, chart versions) in templates, reduce config surface by ~13 paths, drop sealed-secrets component
4. **Modern K8s patterns**: Server-Side Apply with `--field-manager=zhi-glyphoxa`, PodDisruptionBudget with HPA, PSA enforce:restricted namespace labels, scoped NetworkPolicy egress rules
5. **Pre-flight safety**: Infrastructure existence guard in apply.sh prevents confusing crash-loops on first deploy

### Review Findings Incorporated

- P1: Input validators for shell-interpolated values (namespace, release-name, secrets)
- P1: Script files containing secrets deleted via `trap` after apply completes
- P1: Full restricted securityContext + PSA labels on namespace
- P1: `--field-manager=zhi-glyphoxa` on all SSA calls
- P2: Fixed `--all` flag in stop target (was overriding label selector)
- P2: `rm -rf generated/*` before export prevents stale files from disabled components
- P2: `checksum/config` annotation replaces hash-based restart logic
- P2: Pre-flight PostgreSQL check in apply.sh
- P2: Removed vestigial `secrets.yaml.tmpl`
- P2: Added `pods/log` to RBAC Role
- P2: Document manual `zhi component enable mcp-gateway network-policy` step
- P3: Keep topology abstraction (provides clear mental model)
- P3: Keep stop/restart/status apply targets (convenience for TUI operators)
- P3: Drop sealed-secrets component (cluster-wide singleton, install independently)
- P3: Scope NetworkPolicy egress rules (gateway and worker PostgreSQL)

---

# Add zhi-based Deployment Alternative

## Overview

Create a new `deploy/zhi/` directory containing a zhi config plugin (Go) and workspace that generates Kubernetes manifests with full feature parity to the existing Helm chart. Uses a hybrid approach: zhi manages Glyphoxa-specific resources, optional infrastructure (PostgreSQL) is deployed via Helm through a separate apply target.

## Problem Statement / Motivation

The Helm chart works but requires operators to edit YAML values files manually. Zhi provides interactive configuration editing (TUI/Web/MCP), cross-value validation with descriptive error messages, component toggling with dependency tracking, and typed configuration with metadata — advantages for operators who want guided setup rather than raw YAML editing.

## Proposed Solution

A config plugin + workspace following the established `zhi-home-server` pattern:

- **Config plugin** (`deploy/zhi/plugin/`): Go binary implementing `config.Plugin` with all deployment parameters, defaults, metadata for interactive UI, and validation rules
- **Workspace** (`deploy/zhi/workspace/`): Component definitions, export templates generating K8s YAML, and named apply targets for lifecycle management
- **Hybrid infrastructure**: Optional `infrastructure` apply target deploying PostgreSQL via Helm — operators with externally managed infrastructure skip it entirely

## Technical Approach

### Architecture

```
deploy/zhi/
├── plugin/                              # Go config plugin (separate go.mod)
│   ├── main.go                          # hashicorp/go-plugin entry point
│   ├── metadata.go                      # ValueDef helper struct
│   ├── values.go                        # All config paths, defaults, metadata
│   ├── validate.go                      # Validation rules
│   ├── validate_test.go
│   ├── values_test.go
│   ├── go.mod                           # module github.com/MrWong99/zhi-config-glyphoxa
│   ├── go.sum
│   └── zhi-plugin.yaml                  # Plugin manifest
└── workspace/
    ├── zhi.yaml                         # Components, export, apply targets
    ├── zhi-workspace.yaml               # Metadata and dependencies
    ├── templates/
    │   ├── gateway-deployment.yaml.tmpl
    │   ├── gateway-service.yaml.tmpl
    │   ├── mcp-gateway-deployment.yaml.tmpl
    │   ├── mcp-gateway-service.yaml.tmpl
    │   ├── worker-job-template-configmap.yaml.tmpl
    │   ├── configmap.yaml.tmpl
    │   ├── serviceaccount.yaml.tmpl
    │   ├── networkpolicy.yaml.tmpl
    │   ├── hpa.yaml.tmpl
    │   ├── namespace.yaml.tmpl
    │   ├── apply.sh.tmpl
    │   ├── infra.sh.tmpl
    │   └── destroy.sh.tmpl
    ├── generated/                       # Output dir for K8s manifests (gitignored)
    ├── scripts/                         # Output dir for shell scripts (gitignored)
    └── .gitignore
```

### Design Decisions

**1. Complex structured values (tolerations, nodeSelector, resources) as JSON strings**

Zhi's config tree is flat scalar key-value. Structured values like tolerations and nodeSelector are stored as JSON strings in a single config path. Templates use Sprig's `fromJson` to reconstruct:

```
Config path: gateway/node-selector
Default: {}
Template: {{ .GetOr "gateway/node-selector" "{}" | fromJson | toYaml | nindent 8 }}
```

All JSON-typed config values must use `GetOr` with an explicit empty-object/array default to avoid `fromJson` panics on empty strings.

**2. Fixed resource naming prefix with configurable release name**

Config value `core/release-name` (default: `glyphoxa`) controls all resource names. Templates use it as:
- `{{ .Get "core/release-name" }}-gateway` (Deployment, Service)
- `{{ .Get "core/release-name" }}-worker-SESSION_ID` (Job template)
- `{{ .Get "core/release-name" }}-config` (ConfigMap)
- `{{ .Get "core/release-name" }}-admin-key` (Secret — note: differs from Helm's `${RELEASE}-secrets`; migration guide documents rename)

**3. Namespace as explicit config value**

`core/namespace` (default: `glyphoxa`) is embedded in every resource's `metadata.namespace`. The apply script creates the namespace if it doesn't exist and persists it to `.zhi/namespace` for non-export targets.

**4. Image tag is required (blocking validation)**

No equivalent to Helm's `Chart.AppVersion` fallback. The config plugin requires `core/image-tag` to be set, with a blocking validation. This prevents generating manifests with empty image references.

**5. DSN auto-construction in template logic**

When the `postgresql` component is enabled, the template constructs the DSN from `postgresql/*` values:
```
postgres://postgres:{{ .Get "postgresql/postgres-password" }}@{{ .Get "core/release-name" }}-postgresql.{{ .Get "core/namespace" }}.svc:5432/{{ .Get "postgresql/database" }}?sslmode=disable
```
When disabled, uses `database/dsn` directly. Validation blocks if both the component is disabled AND `database/dsn` is empty.

**6. Config data as a YAML string value**

`config/data` holds the Glyphoxa application YAML config as a string. Templates embed it in the ConfigMap. Validation warns if empty (Glyphoxa can run with defaults but likely needs provider config).

**7. ConfigMap-triggered restarts via checksum annotation**

Pod templates include a `checksum/config` annotation computed from `config/data`:
```yaml
annotations:
  checksum/config: {{ .Get "config/data" | sha256sum }}
```
When the ConfigMap content changes, the annotation changes, which causes Kubernetes to see a new pod spec and trigger a rolling update. This is the same pattern used by the Helm chart and works with or without the apply script.

**8. Stop scales to zero; destroy deletes resources**

`stop` runs `kubectl scale --replicas=0` (reversible). `destroy` runs `kubectl delete -f` (permanent). This matches operator expectations.

**9. Topology via config value + validation**

`core/topology` (`shared`|`dedicated`) is a config value. Validation enforces:
- `dedicated` + `gateway/replica-count` > 2 → warning
- `dedicated` + `gateway/pod-anti-affinity` enabled → warning
- Topology sets sensible defaults for node-selector/tolerations labels via validation hints

**10. Store provider: JSON file default, Vault recommended**

The workspace ships with the built-in JSON file store. Documentation recommends Vault for production. The workspace dependency list includes `zhi-store-vault` as optional.

**11. Input validation for shell safety**

All config values interpolated into shell scripts have blocking validators:
- `core/namespace` and `core/release-name`: RFC 1123 DNS label format (`^[a-z0-9][a-z0-9-]{0,62}$`)
- `gateway/admin-key`: reject shell metacharacters (double quotes, backticks, `$`, semicolons, newlines)
- `postgresql/postgres-password`: reject newlines (would break heredoc)

**12. Script file cleanup after apply**

Generated script files (`scripts/apply.sh`, `scripts/infra.sh`) contain interpolated secrets. A `trap` cleanup deletes the entire `scripts/` directory after successful execution:
```bash
trap 'rm -rf ./scripts/' EXIT
```

**13. Full restricted pod security context**

All pod templates include:
```yaml
securityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault
containers:
  - securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
```

The namespace template includes Pod Security Admission labels:
```yaml
labels:
  pod-security.kubernetes.io/enforce: restricted
  pod-security.kubernetes.io/warn: restricted
  pod-security.kubernetes.io/audit: restricted
```

**14. Template variable block for DRY**

Start each template with a variable block to avoid repeated `.Get` calls:
```
{{- $ns := .Get "core/namespace" -}}
{{- $release := .Get "core/release-name" -}}
{{- $topology := .GetOr "core/topology" "shared" -}}
```

### Implementation Phases

#### Phase 1: Config Plugin

Create the Go plugin with all config paths matching the Helm chart's values surface.

**Files to create:**

`deploy/zhi/plugin/go.mod`:
```go
module github.com/MrWong99/zhi-config-glyphoxa

go 1.26.0

require (
    github.com/MrWong99/zhi v1.5.3
    github.com/hashicorp/go-hclog v1.6.3
    github.com/hashicorp/go-plugin v1.7.0
)
```

`deploy/zhi/plugin/metadata.go`: Copy `ValueDef` pattern from `zhi-home-server/plugin/metadata.go` — same struct with `Path`, `Default`, `Section`, `DisplayName`, `Description`, `Type`, `Placeholder`, `Password`, `Required`, `SelectFrom` fields and `ToValue()` method.

`deploy/zhi/plugin/main.go`: Standard zhi config plugin entry point (same boilerplate as `zhi-home-server/plugin/main.go`, name `zhi-config-glyphoxa`).

`deploy/zhi/plugin/values.go`: Define all config paths organized by section. Full path list:

| Section | Path | Default | Type | Notes |
|---|---|---|---|---|
| Core | `core/release-name` | `glyphoxa` | string | Resource naming prefix; RFC 1123 validated |
| Core | `core/namespace` | `glyphoxa` | string | K8s namespace; RFC 1123 validated |
| Core | `core/topology` | `shared` | string | SelectFrom: shared, dedicated |
| Core | `core/image-repository` | `ghcr.io/mrwong99/glyphoxa` | string | |
| Core | `core/image-tag` | `` | string | Required, blocking |
| Core | `core/image-pull-policy` | `IfNotPresent` | string | SelectFrom: Always, IfNotPresent, Never |
| Core | `core/image-pull-secrets` | `[]` | json | JSON array of secret names |
| Gateway | `gateway/replica-count` | `3` | int | |
| Gateway | `gateway/admin-key` | `` | string | Password field; shell metacharacters rejected |
| Gateway | `gateway/resources` | `{"requests":{"cpu":"250m","memory":"256Mi"},"limits":{"cpu":"1","memory":"512Mi"}}` | json | |
| Gateway | `gateway/node-selector` | `{}` | json | |
| Gateway | `gateway/tolerations` | `[]` | json | |
| Gateway | `gateway/pod-anti-affinity` | `true` | bool | |
| Gateway | `gateway/service-account-create` | `true` | bool | |
| Gateway | `gateway/service-account-name` | `glyphoxa-gateway` | string | |
| Gateway | `gateway/service-account-annotations` | `{}` | json | |
| Worker | `worker/resource-profile` | `cloud` | string | SelectFrom: cloud, whisper-native, local-llm |
| Worker | `worker/node-selector` | `{}` | json | |
| Worker | `worker/tolerations` | `[]` | json | |
| Worker | `worker/gpu-node-selector` | `{"nvidia.com/gpu":"true"}` | json | |
| Worker | `worker/gpu-tolerations` | `[{"key":"nvidia.com/gpu","effect":"NoSchedule"}]` | json | |
| MCP Gateway | `mcp-gateway/replica-count` | `2` | int | |
| MCP Gateway | `mcp-gateway/resources` | `{"requests":{"cpu":"250m","memory":"256Mi"},"limits":{"cpu":"1","memory":"512Mi"}}` | json | |
| MCP Gateway | `mcp-gateway/node-selector` | `{}` | json | |
| MCP Gateway | `mcp-gateway/tolerations` | `[]` | json | |
| Config | `config/data` | `` | string | Glyphoxa YAML config blob |
| Database | `database/dsn` | `` | string | Password field |
| Autoscaling | `autoscaling/min-replicas` | `2` | int | |
| Autoscaling | `autoscaling/max-replicas` | `10` | int | |
| Autoscaling | `autoscaling/target-cpu` | `70` | int | |
| PostgreSQL | `postgresql/postgres-password` | `` | string | Password, required; newlines rejected |
| PostgreSQL | `postgresql/database` | `glyphoxa` | string | |
| PostgreSQL | `postgresql/persistence-size` | `10Gi` | string | |

**Hardcoded in templates (not config paths):** Ports (8081, 50051, 9090, 8080), worker deadlines (14400s active, 300s TTL), chart versions (PostgreSQL 16.4.11). These are stable implementation details.

**Worker profiles hardcoded in template:** The three resource profiles (cloud, whisper-native, local-llm) are fixed resource specifications. Hardcoded in the worker job template with a `{{ if eq $profile "cloud" }}...{{ else if }}` chain. The operator only picks a profile name.

`deploy/zhi/plugin/validate.go`: Validation functions:

```go
// validateNamespace — blocking if not matching ^[a-z0-9][a-z0-9-]{0,62}$ (RFC 1123)
// validateReleaseName — blocking if not matching ^[a-z0-9][a-z0-9-]{0,62}$ (RFC 1123)
// validateTopology — blocking if not "shared" or "dedicated"
// validateImageTag — blocking if empty
// validateDSN — blocking if postgresql component disabled AND dsn empty
// validateReplicaCountDedicated — warning if topology=dedicated AND replica-count > 2
// validateAntiAffinityDedicated — warning if topology=dedicated AND pod-anti-affinity=true
// validateResourceProfile — blocking if not cloud/whisper-native/local-llm
// validateGPUTolerations — warning if resource-profile=local-llm AND gpu-tolerations empty
// validatePostgresPassword — blocking if postgresql component enabled AND password empty; blocking if contains newlines
// validateAdminKey — warning if empty ("admin API will be unauthenticated"); blocking if contains shell metacharacters
// validateJSON — blocking if value is not valid JSON (for json-typed fields)
```

`deploy/zhi/plugin/zhi-plugin.yaml`:
```yaml
schemaVersion: "1"
name: glyphoxa
type: config
version: 0.1.0
zhiProtocolVersion: "1"
description: Configuration plugin for Glyphoxa Kubernetes deployment.
author: MrWong99
license: MIT
homepage: https://github.com/MrWong99/glyphoxa
keywords:
  - config
  - kubernetes
  - glyphoxa
binaries:
  linux/amd64: dist/zhi-config-glyphoxa_linux_amd64
  linux/arm64: dist/zhi-config-glyphoxa_linux_arm64
  darwin/amd64: dist/zhi-config-glyphoxa_darwin_amd64
  darwin/arm64: dist/zhi-config-glyphoxa_darwin_arm64
```

`deploy/zhi/plugin/values_test.go`: Test that:
- `List()` returns all expected paths
- `Get()` returns correct defaults and metadata for each path
- `Set()` persists values
- All paths have valid metadata (`ui.section`, `ui.displayName`, `core.description`, `core.type`)
- All tests use `t.Parallel()`

`deploy/zhi/plugin/validate_test.go`: Test each validation function:
- `validateNamespace` blocks on `"INVALID"`, `"a b"`, `"ns;rm -rf /"`, passes on `"glyphoxa"`, `"my-ns-123"`
- `validateReleaseName` same pattern
- `validateTopology` blocks on "invalid", passes on "shared"/"dedicated"
- `validateImageTag` blocks on empty
- `validateDSN` blocks when postgresql component disabled and DSN empty
- `validateReplicaCountDedicated` warns when topology=dedicated and count=5
- `validateAdminKey` blocks on values with backticks, `$(...)`, double quotes
- `validatePostgresPassword` blocks on values containing newlines
- `validateJSON` blocks on malformed JSON
- Cross-value validations use mock TreeReader
- All tests use `t.Parallel()`

**Success criteria:**
- [ ] `cd deploy/zhi/plugin && go build` produces `zhi-config-glyphoxa` binary
- [ ] `cd deploy/zhi/plugin && go test -race -count=1 ./...` passes
- [ ] Plugin starts and responds to gRPC calls via `zhi list`, `zhi get`
- [ ] All config paths are listed with correct defaults and metadata
- [ ] Shell injection attempts are blocked by validators

#### Phase 2: Workspace Configuration

Create the workspace definition with components, export templates, and apply targets.

**Files to create:**

`deploy/zhi/workspace/zhi.yaml`:
```yaml
version: "1"

config:
  provider: glyphoxa

store:
  provider: json
  options:
    directory: ./.zhi/store

components:
  - name: core
    description: "Core settings, gateway, worker, config, and database"
    paths: ["core/", "gateway/", "worker/", "config/", "database/"]
    mandatory: true

  - name: mcp-gateway
    description: "MCP tool server gateway"
    paths: ["mcp-gateway/"]

  - name: network-policy
    description: "Kubernetes NetworkPolicies"
    paths: ["network-policy/"]

  - name: autoscaling
    description: "Horizontal Pod Autoscaler for gateway"
    paths: ["autoscaling/"]

  - name: postgresql
    description: "PostgreSQL via Helm (infrastructure target)"
    paths: ["postgresql/"]

export:
  templates:
    - name: namespace
      template: ./templates/namespace.yaml.tmpl
      output: ./generated/namespace.yaml
    - name: serviceaccount
      template: ./templates/serviceaccount.yaml.tmpl
      output: ./generated/serviceaccount.yaml
    - name: configmap
      template: ./templates/configmap.yaml.tmpl
      output: ./generated/configmap.yaml
    - name: gateway-deployment
      template: ./templates/gateway-deployment.yaml.tmpl
      output: ./generated/gateway-deployment.yaml
    - name: gateway-service
      template: ./templates/gateway-service.yaml.tmpl
      output: ./generated/gateway-service.yaml
    - name: worker-job-template
      template: ./templates/worker-job-template-configmap.yaml.tmpl
      output: ./generated/worker-job-template.yaml
    - name: mcp-gateway-deployment
      template: ./templates/mcp-gateway-deployment.yaml.tmpl
      output: ./generated/mcp-gateway-deployment.yaml
    - name: mcp-gateway-service
      template: ./templates/mcp-gateway-service.yaml.tmpl
      output: ./generated/mcp-gateway-service.yaml
    - name: networkpolicy
      template: ./templates/networkpolicy.yaml.tmpl
      output: ./generated/networkpolicy.yaml
    - name: hpa
      template: ./templates/hpa.yaml.tmpl
      output: ./generated/hpa.yaml
    - name: apply-script
      template: ./templates/apply.sh.tmpl
      output: ./scripts/apply.sh
    - name: infra-script
      template: ./templates/infra.sh.tmpl
      output: ./scripts/infra.sh
    - name: destroy-script
      template: ./templates/destroy.sh.tmpl
      output: ./scripts/destroy.sh

apply:
  targets:
    default:
      command: "bash ./scripts/apply.sh"
      workdir: "."
      pre-export: true
      timeout: 300
    infrastructure:
      command: "bash ./scripts/infra.sh"
      workdir: "."
      pre-export: true
      timeout: 300
    stop:
      command: "kubectl scale deployment -l app.kubernetes.io/part-of=glyphoxa --replicas=0 -n $(cat .zhi/namespace 2>/dev/null || echo glyphoxa)"
      workdir: "."
      pre-export: false
      timeout: 120
    destroy:
      command: "bash ./scripts/destroy.sh"
      workdir: "."
      pre-export: true
      timeout: 120
    restart:
      command: "kubectl rollout restart deployment -l app.kubernetes.io/part-of=glyphoxa -n $(cat .zhi/namespace 2>/dev/null || echo glyphoxa)"
      workdir: "."
      pre-export: false
      timeout: 120
    status:
      command: "kubectl get all -l app.kubernetes.io/part-of=glyphoxa -n $(cat .zhi/namespace 2>/dev/null || echo glyphoxa)"
      workdir: "."
      pre-export: false
      timeout: 30
```

**Default component state:** Zhi components start disabled by default. To match Helm defaults (`mcpGateway.enabled: true`, `networkPolicy.enabled: true`), the README documents that operators should run `zhi component enable mcp-gateway network-policy` after first setup.

`deploy/zhi/workspace/zhi-workspace.yaml`:
```yaml
name: glyphoxa-k8s
version: 0.1.0
description: Kubernetes deployment workspace for Glyphoxa voice NPC framework.
author: MrWong99
license: MIT
dependencies:
  - ref: oci://ghcr.io/mrwong99/glyphoxa/zhi-config-glyphoxa:latest
    type: config
    optional: false
  - ref: oci://ghcr.io/mrwong99/zhi/zhi-store-vault:latest
    type: store
    optional: true
tools:
  - name: kubectl
    version: "1.28"
  - name: helm
    version: "3.12"
keywords:
  - kubernetes
  - glyphoxa
  - voice-npc
```

`deploy/zhi/workspace/.gitignore`:
```
generated/
scripts/
.zhi/
```

**Success criteria:**
- [ ] `zhi.yaml` is valid and loads in zhi
- [ ] Components list correctly with `zhi component list`
- [ ] Component toggling works (enable/disable mcp-gateway, etc.)

#### Phase 3: Kubernetes Manifest Templates

Create all K8s YAML templates matching the Helm chart's output. Each template uses zhi's TreeData API (`.Get`, `.GetOr`, `.Has`, `.ComponentEnabled`) plus Sprig functions (`fromJson`, `toYaml`, `toJson`, `quote`, `nindent`, `sha256sum`).

**Template-to-Helm mapping with key implementation details:**

`templates/namespace.yaml.tmpl`:
- Simple Namespace resource using `.Get "core/namespace"`
- PSA labels: `pod-security.kubernetes.io/enforce: restricted`, `warn: restricted`, `audit: restricted`

`templates/gateway-deployment.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/gateway-deployment.yaml`
- Conditional `spec.replicas` — omit when autoscaling component enabled
- `checksum/config` annotation: `{{ .Get "config/data" | sha256sum }}`
- Pod anti-affinity block conditional on `gateway/pod-anti-affinity`
- `nodeSelector` and `tolerations` via `GetOr ... "{}" | fromJson | toYaml`
- `resources` via `GetOr ... "{}" | fromJson | toYaml`
- Full restricted `securityContext` on pod and container
- ConfigMap volume mount conditional on `config/data` being non-empty
- Environment variables: `GLYPHOXA_ADMIN_KEY` from Secret (`${RELEASE}-admin-key`), `GLYPHOXA_GRPC_ADDR`, `GLYPHOXA_DATABASE_DSN`
- DSN construction: if postgresql component enabled, build from postgresql values; else use `database/dsn`
- Health probes: `/healthz`, `/readyz`, startup probe on observe port (9090)
- Labels: `app.kubernetes.io/name`, `app.kubernetes.io/instance` (= release-name), `app.kubernetes.io/component: gateway`, `app.kubernetes.io/part-of: glyphoxa`, `app.kubernetes.io/managed-by: zhi`, `app.kubernetes.io/version` (= image-tag)

`templates/gateway-service.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/service.yaml`
- ClusterIP service with admin, grpc, observe ports (hardcoded: 8081, 50051, 9090)

`templates/mcp-gateway-deployment.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/mcp-gateway-deployment.yaml`
- Entire file wrapped in `{{ if .ComponentEnabled "mcp-gateway" }}`
- Same pattern as gateway: `checksum/config` annotation, restricted `securityContext`
- `--mode=mcp-gateway`, `GLYPHOXA_MCP_ADDR` env var

`templates/mcp-gateway-service.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/mcp-gateway-service.yaml`
- Conditional on mcp-gateway component

`templates/worker-job-template-configmap.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/worker-job.yaml`
- ConfigMap containing a Job YAML template with `SESSION_ID` placeholder
- Worker resource profiles hardcoded in template with `{{ if eq $profile "cloud" }}...{{ else if }}` chain
- Restricted `securityContext` on worker container
- GPU merge logic: when profile=local-llm, merge gpu-node-selector and gpu-tolerations
- `GLYPHOXA_GATEWAY_ADDR`: `{{ $release }}-gateway.{{ $ns }}.svc:50051`
- `GLYPHOXA_MCP_GATEWAY_URL`: conditional on mcp-gateway component
- Worker only has `startupProbe` (no liveness/readiness — correct for Job workloads)

`templates/configmap.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/configmap.yaml`
- Conditional on `config/data` being non-empty
- Embeds the YAML string directly

`templates/serviceaccount.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/serviceaccount.yaml`
- Conditional on `gateway/service-account-create`
- Includes ServiceAccount, Role, RoleBinding
- Role verbs: `create`, `get`, `list`, `watch`, `delete` for `batch/jobs`; `get`, `list`, `watch` for `pods` and `pods/log`
- `automountServiceAccountToken: false` on MCP gateway and worker pods

`templates/networkpolicy.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/networkpolicy.yaml`
- Entire file wrapped in `{{ if .ComponentEnabled "network-policy" }}`
- **Gateway policy**: admin ingress from namespace, gRPC ingress from workers, observe ingress from any; egress scoped to DNS (53), PostgreSQL (5432 scoped to pod selector when postgresql component enabled), K8s API (6443), HTTPS (443)
- **Worker policy**: gRPC ingress from gateway, observe ingress from any; egress DNS/gateway/PostgreSQL (scoped to pod selector)/mcp/HTTPS
- **MCP Gateway policy**: conditional on mcp-gateway component enabled

`templates/hpa.yaml.tmpl`:
- Reference: `deploy/helm/glyphoxa/templates/hpa.yaml`
- Entire file wrapped in `{{ if .ComponentEnabled "autoscaling" }}`
- Targets gateway deployment by name
- Includes PodDisruptionBudget with `maxUnavailable: 1`

**Success criteria:**
- [ ] `zhi export` generates all manifest files in `generated/`
- [ ] Generated manifests pass `kubectl apply --dry-run=server`
- [ ] Output matches Helm chart output for equivalent values (spot-check key resources)
- [ ] Component toggling correctly includes/excludes optional resources
- [ ] All pod specs include restricted securityContext

#### Phase 4: Apply Scripts

Create the shell script templates for lifecycle management.

`templates/apply.sh.tmpl`:
```bash
#!/usr/bin/env bash
set -euo pipefail

# Clean generated/ to prevent stale files from disabled components.
rm -rf ./generated/*

NAMESPACE="{{ .Get "core/namespace" }}"
RELEASE="{{ .Get "core/release-name" }}"

# Delete script files containing secrets on exit.
trap 'rm -rf ./scripts/' EXIT

# Persist namespace for non-export targets (stop, restart, status).
mkdir -p .zhi
echo "$NAMESPACE" > .zhi/namespace

# Create namespace if it doesn't exist.
kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || \
  kubectl create namespace "$NAMESPACE"

{{ if .ComponentEnabled "postgresql" -}}
# Pre-flight: verify PostgreSQL is running (prevents confusing crash-loop).
if ! kubectl get statefulset "${RELEASE}-postgresql" -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "ERROR: PostgreSQL not found. Run 'zhi apply infrastructure' first."
  exit 1
fi
{{ end -}}

# Create secret (avoids plaintext in generated/ directory).
{{ if .Has "gateway/admin-key" -}}
kubectl create secret generic "${RELEASE}-admin-key" \
  --from-literal=admin-key="{{ .Get "gateway/admin-key" }}" \
  --namespace "$NAMESPACE" \
  --dry-run=client -o yaml | kubectl apply --server-side --field-manager=zhi-glyphoxa -f -
{{ end -}}

# Apply all generated manifests.
kubectl apply --server-side --field-manager=zhi-glyphoxa -f ./generated/ -n "$NAMESPACE"

# Wait for rollout.
kubectl rollout status deployment "${RELEASE}-gateway" -n "$NAMESPACE" --timeout=120s
{{ if .ComponentEnabled "mcp-gateway" -}}
kubectl rollout status deployment "${RELEASE}-mcp-gateway" -n "$NAMESPACE" --timeout=120s
{{ end -}}

echo "Glyphoxa deployed to namespace ${NAMESPACE}"
```

`templates/infra.sh.tmpl`:
```bash
#!/usr/bin/env bash
set -euo pipefail
umask 077

NAMESPACE="{{ .Get "core/namespace" }}"
RELEASE="{{ .Get "core/release-name" }}"

# Delete script files containing secrets on exit.
trap 'rm -rf ./scripts/' EXIT

kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || \
  kubectl create namespace "$NAMESPACE"

{{ if .ComponentEnabled "postgresql" -}}
echo "Deploying PostgreSQL..."
TMPVALS=$(mktemp)
trap 'rm -f "$TMPVALS"; rm -rf ./scripts/' EXIT
cat > "$TMPVALS" <<'ZHI_HELM_VALUES_EOF'
auth:
  postgresPassword: "{{ .Get "postgresql/postgres-password" }}"
  database: "{{ .Get "postgresql/database" }}"
primary:
  persistence:
    size: "{{ .Get "postgresql/persistence-size" }}"
ZHI_HELM_VALUES_EOF
helm upgrade --install "${RELEASE}-postgresql" \
  oci://registry-1.docker.io/bitnamicharts/postgresql \
  --version "16.4.11" \
  --namespace "$NAMESPACE" \
  --values "$TMPVALS" \
  --wait --timeout 5m
{{ end -}}

echo "Infrastructure deployment complete."
```

`templates/destroy.sh.tmpl`:
```bash
#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="{{ .Get "core/namespace" }}"
RELEASE="{{ .Get "core/release-name" }}"

# Delete script files containing secrets on exit.
trap 'rm -rf ./scripts/' EXIT

echo "Destroying Glyphoxa resources in namespace ${NAMESPACE}..."

# Delete zhi-managed resources.
kubectl delete -f ./generated/ -n "$NAMESPACE" --ignore-not-found

# Delete secret.
kubectl delete secret "${RELEASE}-admin-key" -n "$NAMESPACE" --ignore-not-found

{{ if .ComponentEnabled "postgresql" -}}
echo "Uninstalling PostgreSQL Helm release..."
helm uninstall "${RELEASE}-postgresql" -n "$NAMESPACE" 2>/dev/null || true
# Note: PVCs are intentionally preserved. Delete manually if desired:
# kubectl delete pvc -n "$NAMESPACE" -l app.kubernetes.io/instance=${RELEASE}-postgresql
{{ end -}}

# Clean local state.
rm -f .zhi/namespace

echo "Destruction complete. Namespace ${NAMESPACE} preserved (delete manually if desired)."
```

**First deploy ordering:** Infrastructure target MUST run before app target on first deploy (PostgreSQL needs to be up before gateway tries to connect). The apply script enforces this with a pre-flight check when the postgresql component is enabled.

```
# First deploy:
zhi apply infrastructure   # PostgreSQL
zhi apply                  # Glyphoxa app resources

# Subsequent deploys:
zhi apply                  # App resources only (infra already running)
```

**Helm-to-zhi migration path:** For operators migrating from the Helm chart:
1. `helm get values glyphoxa -o yaml` to export current values
2. Map Helm values to zhi config paths via `zhi edit`
3. `zhi apply` — Server-Side Apply with `--field-manager=zhi-glyphoxa` handles ownership transfer from Helm to zhi
4. Verify all resources healthy
5. `helm uninstall glyphoxa --keep-history` to clean up Helm metadata (resources persist because SSA transferred ownership)
6. Note: admin key secret name changes from `${RELEASE}-secrets` to `${RELEASE}-admin-key` — the old secret can be deleted after migration

**Success criteria:**
- [ ] `zhi apply` exports templates then runs `apply.sh` successfully against a cluster
- [ ] `zhi apply infrastructure` deploys PostgreSQL via Helm
- [ ] `zhi apply stop` scales deployments to 0
- [ ] `zhi apply restart` restarts deployments
- [ ] `zhi apply destroy` cleans up all resources
- [ ] `zhi apply status` shows deployment state
- [ ] Script files are deleted after execution (trap cleanup)
- [ ] Pre-flight check prevents deploy without infrastructure

#### Phase 5: Tests and Documentation

**Plugin tests** (already outlined in Phase 1):
- `values_test.go`: All paths listed, defaults correct, metadata complete
- `validate_test.go`: Each validator with positive and negative cases, including shell injection attempts
- All tests use `t.Parallel()` and table-driven patterns

**Template smoke tests**:
- Add a CI step that builds the plugin, sets required values (`core/image-tag`), runs `zhi export`, and validates generated YAML with `kubectl apply --dry-run=client`

**Documentation**:
- `deploy/zhi/README.md` with:
  - Prerequisites (zhi, kubectl, helm for infrastructure)
  - Quick start: build plugin → `cd workspace` → `zhi component enable mcp-gateway network-policy` → `zhi edit` → configure → `zhi apply infrastructure` → `zhi apply`
  - Component reference table
  - Apply target reference table
  - Comparison with Helm approach
  - Migration guide from Helm chart (including secret rename)
  - Production recommendations (Vault store, admin key, etc.)
  - Security notes: script files are auto-deleted, JSON store warning, restricted pod security

**Success criteria:**
- [ ] All tests pass with `t.Parallel()` and `-race -count=1`
- [ ] README has quick start and migration instructions
- [ ] A fresh operator can deploy Glyphoxa using only the README instructions

## Acceptance Criteria

### Functional Requirements

- [ ] Config plugin defines all operator-facing parameters from the Helm chart's `values.yaml`
- [ ] `zhi edit` provides interactive configuration with sections, descriptions, and dropdowns
- [ ] `zhi validate` catches configuration errors (missing image tag, invalid topology, shell injection in namespace/release-name, etc.)
- [ ] `zhi export` generates valid Kubernetes manifests for all enabled components
- [ ] `zhi apply` deploys Glyphoxa to a Kubernetes cluster
- [ ] `zhi apply infrastructure` deploys PostgreSQL via Helm (password not exposed in `ps`)
- [ ] Shared topology: 3 gateway replicas, anti-affinity, shared node selector/tolerations
- [ ] Dedicated topology: 1 gateway replica, no anti-affinity, dedicated node selector/tolerations
- [ ] Component toggling: disabling mcp-gateway removes its Deployment, Service, and NetworkPolicy
- [ ] Worker resource profiles (cloud/whisper-native/local-llm) produce correct resource limits
- [ ] GPU scheduling works when local-llm profile is selected
- [ ] Secrets are created via `kubectl` (not written as plaintext YAML in `generated/`)
- [ ] Script files containing secrets are deleted after execution
- [ ] All pods run with restricted security context (runAsNonRoot, no privilege escalation, drop all caps)
- [ ] Pre-flight check prevents deploying app before infrastructure

### Non-Functional Requirements

- [ ] Plugin builds with `CGO_ENABLED=0` (no system dependencies)
- [ ] Plugin binary size < 20MB
- [ ] Template export completes in < 2s
- [ ] A GitHub workflow builds the plugin 
- [ ] a separate GitHub workflow releases both the plugin and the workspace to ghcr.io/mrwong99/glyphoxa/...

### Quality Gates

- [ ] `go test -race -count=1 ./...` passes in plugin directory (all tests use `t.Parallel()`)
- [ ] `golangci-lint run` passes in plugin directory
- [ ] Generated manifests pass `kubectl apply --dry-run=server` against a test cluster

## Dependencies & Prerequisites

- zhi v1.5.3+ installed
- kubectl configured with cluster access (K8s 1.22+ for Server-Side Apply)
- Helm 3.12+ (only for infrastructure target)
- Go 1.26+ (for building the plugin from source)

## Risk Analysis & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| JSON string values are awkward in TUI | Medium | Medium | Add `core.type: json` metadata hint so UI can render a structured editor; fallback to multiline text |
| Template logic for worker profiles is complex | Low | Medium | Hardcode profiles in template with if/else chain; test with all three profiles |
| PostgreSQL service name mismatch between infra.sh and DSN | Medium | High | Use consistent naming: `${RELEASE}-postgresql` in both infra.sh and DSN template logic |
| Operators forget to run infrastructure target | Low | Low | Pre-flight check in apply.sh blocks deploy if PostgreSQL not found |
| Plaintext secrets in JSON file store | Medium | High | Validation warning recommending Vault; document in README |
| Helm-to-zhi migration breaks existing deployment | Low | High | Server-Side Apply with `--field-manager=zhi-glyphoxa` handles ownership transfer; `--keep-history` on uninstall; document migration path |
| Secret name change (`-secrets` → `-admin-key`) during migration | Low | Medium | Document rename in migration guide |

## References & Research

### Internal References

- Brainstorm: `docs/brainstorms/2026-03-08-zhi-deployment-brainstorm.md`
- Helm chart: `deploy/helm/glyphoxa/` (all templates, values, helpers)
- Helm values: `deploy/helm/glyphoxa/values.yaml`
- Helm helpers: `deploy/helm/glyphoxa/templates/_helpers.tpl`
- Tenant provisioning: `deploy/scripts/provision-dedicated-tenant.sh`

### Reference Implementations

- zhi-home-server plugin: `/home/luk/Desktop/git/zhi-home-server/plugin/`
- zhi-home-server workspace: `/home/luk/Desktop/git/zhi-home-server/workspace/`
- zhi TreeData API: `/home/luk/Desktop/git/zhi/internal/core/export_data.go`
- zhi export engine: `/home/luk/Desktop/git/zhi/internal/core/export.go`

### Conventions

- Plugin module: `github.com/MrWong99/zhi-config-glyphoxa`
- Plugin binary: `zhi-config-glyphoxa`
- Plugin handshake: `ZHI_PLUGIN: zhiplugin-v1`
- Static linking: `CGO_ENABLED=0`
- Testing: `-race -count=1`, `t.Parallel()`, table-driven

### Review Todos

All review findings are tracked in `todos/001-017-pending-*.md`.
