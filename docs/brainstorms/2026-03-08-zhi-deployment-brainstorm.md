---
date: 2026-03-08
topic: zhi-deployment
---

# Zhi-based Deployment for Glyphoxa

## What We're Building

A new `deploy/zhi/` directory containing an alternative deployment approach using zhi. This includes:

1. A **zhi config plugin** (Go) defining all deployment parameters currently spread across `values.yaml`, `values-shared.yaml`, and `values-dedicated.yaml`
2. A **zhi workspace** with Go templates generating raw Kubernetes YAML manifests matching the Helm chart's full resource set
3. **Apply targets** for deployment lifecycle (apply, stop, destroy, restart, infrastructure)
4. A **hybrid strategy** where zhi owns Glyphoxa resources and optional infrastructure components (PostgreSQL, sealed-secrets) are deployed via Helm through a separate apply target

## Why This Approach

The Helm chart works but zhi offers interactive configuration editing (TUI/Web/MCP), cross-value validation, component toggling, and a typed config plugin — advantages for operators who want guided setup. The hybrid approach (Approach A) was chosen because:

- Zhi handles Glyphoxa-specific resources with full validation and metadata
- Infrastructure components (PostgreSQL, sealed-secrets) stay as upstream Helm charts — battle-tested and maintained by their communities
- Operators who manage infrastructure separately just skip the `infrastructure` apply target
- Helm is already a known tool in the ecosystem (it's the current deployment method)

## Key Decisions

### Topology: Single workspace with topology value (not separate workspaces)
A `core/topology` config value (`shared` or `dedicated`) controls replica counts, anti-affinity, node selectors via template conditionals. The config plugin validates topology-specific constraints (e.g., warn if dedicated has 3+ replicas). Transform or validation logic enforces topology defaults.

**Rationale:** The topology differences are value-level (replica count, node labels, affinity toggles), not structural. One workspace is simpler to maintain.

### Components
| Component | Paths | Mandatory | Dependencies |
|---|---|---|---|
| `gateway` | `gateway/` | Yes | — |
| `worker` | `worker/` | Yes | — |
| `mcp-gateway` | `mcp-gateway/` | No | — |
| `network-policy` | `network-policy/` | No | — |
| `autoscaling` | `autoscaling/` | No | — |
| `postgresql` | `postgresql/` | No | — |
| `sealed-secrets` | `sealed-secrets/` | No | — |

### Infrastructure via Helm (opt-in)
The `infrastructure` apply target runs `helm upgrade --install` for PostgreSQL and sealed-secrets using config values from the zhi tree (chart version, passwords, persistence size, etc.). Operators with externally managed infrastructure skip this target entirely and just provide a `database/dsn`.

### Feature Parity with Helm Chart
The following Kubernetes resources will be generated via zhi templates:

**Always generated:**
- Gateway Deployment + Service
- Worker Job Template ConfigMap
- Application ConfigMap (`/etc/glyphoxa/config.yaml`)
- ServiceAccount + Role + RoleBinding (job-manager RBAC)
- Secrets (admin API key)

**Conditional on components:**
- MCP Gateway Deployment + Service (component: `mcp-gateway`)
- NetworkPolicies for gateway, worker, mcp-gateway (component: `network-policy`)
- HorizontalPodAutoscaler (component: `autoscaling`)

**Via infrastructure apply target:**
- PostgreSQL (Bitnami Helm chart)
- Sealed-secrets controller (Bitnami Labs Helm chart)

### Config Plugin Structure

The plugin defines values mirroring the Helm values with metadata for interactive editing:

- `core/` — namespace, topology (shared/dedicated), image repo/tag/pullPolicy
- `gateway/` — replicaCount, ports, resources, adminKey, podAntiAffinity, nodeSelector, tolerations, serviceAccount
- `worker/` — resourceProfile (cloud/whisper-native/local-llm), profiles, activeDeadlineSeconds, ttlSecondsAfterFinished, ports, nodeSelector, tolerations, gpu settings
- `mcp-gateway/` — replicaCount, ports, resources, nodeSelector, tolerations
- `database/` — dsn (manual or auto-built from postgresql component values)
- `network-policy/` — enabled flag (component toggle handles this)
- `autoscaling/` — minReplicas, maxReplicas, targetCPUUtilizationPercentage
- `postgresql/` — auth (postgresPassword, database), persistence size, chart version
- `sealed-secrets/` — chart version

### Validation Rules
- `core/topology` must be `shared` or `dedicated`
- `gateway/replicaCount` warning if >2 in dedicated topology
- `gateway/adminKey` required (blocking) for non-dev deployments
- `database/dsn` required if `postgresql` component is disabled
- `worker/resourceProfile` must be one of `cloud`, `whisper-native`, `local-llm`
- Cross-value: if `worker/resourceProfile` is `local-llm`, warn if no GPU tolerations set

### Apply Targets
```yaml
apply:
  default:
    command: "./scripts/apply.sh"
    pre-export: true
    timeout: 300
  infrastructure:
    command: "./scripts/infra.sh"
    pre-export: true
    timeout: 300
  stop:
    command: "kubectl delete -f ./generated/ --ignore-not-found"
    pre-export: false
  destroy:
    command: "./scripts/destroy.sh"
    pre-export: false
  restart:
    command: "kubectl rollout restart deployment -l app.kubernetes.io/part-of=glyphoxa"
    pre-export: false
```

### Template Structure
```
deploy/zhi/
├── plugin/
│   ├── main.go
│   ├── metadata.go          # ValueDef helper (same pattern as home-server)
│   ├── values.go             # All config paths, defaults, metadata
│   ├── validate.go           # Validation rules
│   ├── validate_test.go
│   ├── values_test.go
│   ├── go.mod
│   └── zhi-plugin.yaml
├── workspace/
│   ├── zhi.yaml              # Components, export, apply targets
│   ├── zhi-workspace.yaml    # Metadata and dependencies
│   ├── templates/
│   │   ├── gateway-deployment.yaml.tmpl
│   │   ├── gateway-service.yaml.tmpl
│   │   ├── mcp-gateway-deployment.yaml.tmpl
│   │   ├── mcp-gateway-service.yaml.tmpl
│   │   ├── worker-job-template-configmap.yaml.tmpl
│   │   ├── configmap.yaml.tmpl
│   │   ├── secrets.yaml.tmpl
│   │   ├── serviceaccount.yaml.tmpl
│   │   ├── networkpolicy.yaml.tmpl
│   │   ├── hpa.yaml.tmpl
│   │   ├── apply.sh.tmpl
│   │   ├── infra.sh.tmpl
│   │   └── destroy.sh.tmpl
│   ├── scripts/              # Generated by export (gitignored)
│   ├── generated/            # Generated K8s manifests (gitignored)
│   └── .gitignore
```

## Open Questions

- Should the zhi workspace publish to GHCR as an OCI artifact (like zhi-home-server), or is it only consumed from the monorepo?
- Should there be a CI workflow that validates the plugin builds and templates render correctly?
- Do we want a `status` apply target that runs `kubectl get` commands to show deployment state?

## Next Steps

-> `/workflows:plan` for implementation details
