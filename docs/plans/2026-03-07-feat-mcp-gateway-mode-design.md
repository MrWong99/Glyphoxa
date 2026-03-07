---
title: "feat: MCP Gateway Mode"
type: feat
status: completed
date: 2026-03-07
---

# MCP Gateway Mode

## Problem

In multi-tenant deployments, each worker pod is ephemeral (one per Discord
session). Every worker currently creates its own `mcphost.Host`, spawning
duplicate stdio MCP server subprocesses and opening duplicate HTTP
connections to external MCP servers. With 20 concurrent sessions, that means
20 copies of every MCP server — wasted memory, CPU, and file descriptors.

## Solution

Add `--mode=mcp-gateway` to the Glyphoxa binary. The MCP gateway runs a
real MCP server over the Streamable HTTP protocol (using the official Go
SDK's `mcp.NewStreamableHTTPHandler`). It hosts all stateless tools behind
a single endpoint. Workers connect to it as just another MCP server via
`StreamableClientTransport` — zero changes to the worker's tool execution
flow.

## Architecture

```
Workers ──StreamableHTTP──> MCP Gateway ──> Built-in tools (dice, rules, etc.)
                                       ──> External stdio MCP servers (shared pool)
                                       ──> External HTTP MCP servers (proxied)
```

### What the MCP gateway hosts

- **Built-in stateless Go tools**: dice roller, rules lookup, and any
  future stateless tools registered via `RegisterBuiltin`
- **External stdio MCP servers**: spawned once as shared subprocesses,
  not per-worker
- **External HTTP MCP servers**: proxied through a single connection pool

### What stays on the worker

The **memory tool** and any future tools requiring per-tenant database
access. Workers create a local `mcphost.Host` for these tenant-scoped
tools. The MCP gateway appears as an additional registered server on the
worker's host — the existing `RegisterServer` + `ExecuteTool` flow handles
merging transparently.

### Key properties

- **Stateless**: No tenant context, no database connection. Pure function
  tool execution (input -> output).
- **Horizontally scalable**: Any number of replicas behind a Service.
- **Shared calibration**: Latency/health data is measured once on the
  gateway and shared across all workers implicitly (workers see the
  gateway's tools with their real latency characteristics).
- **No new protocols**: Uses the MCP Streamable HTTP protocol from the
  official Go SDK. Workers already support `StreamableClientTransport`.

## Implementation

### Binary mode: `runMCPGateway(cfg)`

New function in `cmd/glyphoxa/main.go` following the existing
`runFull`/`runGateway`/`runWorker` pattern:

1. Create an `mcphost.Host` and register built-in stateless tools +
   external MCP servers from config (same registration flow as `runFull`,
   minus the memory tool)
2. Run `Calibrate()` on startup
3. Create an `mcp.Server` (Go SDK) and register each tool from the host
4. Serve via `mcp.NewStreamableHTTPHandler` on `:8080` (configurable)
5. Start the observe server on `:9090` for `/healthz`, `/readyz`, `/metrics`
6. Handle graceful shutdown (close MCP host, drain HTTP connections)

```go
// Pseudocode for runMCPGateway
func runMCPGateway(cfg *config.Config) int {
    host := mcphost.New()
    // Register built-in stateless tools (dice, rules, etc.)
    // Register external MCP servers from config
    // Calibrate

    server := mcp.NewServer(...)
    // For each tool in host.AvailableTools(BudgetDeep):
    //   mcp.AddTool(server, tool, handler that calls host.ExecuteTool)

    handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
        return server
    }, nil)

    // Serve on :8080, observe on :9090, graceful shutdown
}
```

### Worker side changes

Minimal. The worker checks for `GLYPHOXA_MCP_GATEWAY_URL` env var at
startup. If set, it registers the MCP gateway as a `streamable-http`
server on its local `mcphost.Host`:

```go
if mcpGatewayURL := os.Getenv("GLYPHOXA_MCP_GATEWAY_URL"); mcpGatewayURL != "" {
    err := host.RegisterServer(ctx, mcp.ServerConfig{
        Name:      "mcp-gateway",
        Transport: mcp.TransportStreamableHTTP,
        URL:       mcpGatewayURL,
    })
}
```

The worker still registers tenant-scoped tools (memory tool) locally.
Budget tier filtering happens on the worker side as it does today.

### Ports

| Port | Purpose |
|------|---------|
| 8080 | MCP Streamable HTTP endpoint |
| 9090 | Observe server (`/healthz`, `/readyz`, `/metrics`) |

## Kubernetes Deployment

### Helm templates

- **`mcp-gateway-deployment.yaml`**: Deployment with 2 replicas
- **`mcp-gateway-service.yaml`**: ClusterIP Service on ports 8080 and 9090
- **NetworkPolicy addition**: Workers can reach MCP gateway on `:8080`;
  MCP gateway can reach external APIs (HTTPS, port 443) and DNS; no access
  to PostgreSQL (it has no tenant data)
- **Worker Job template update**: Add `GLYPHOXA_MCP_GATEWAY_URL` env var
  pointing to the MCP gateway Service

### Resource profile

The MCP gateway is lightweight — no audio processing, no LLM calls, just
tool proxying and stdio subprocess management:

```yaml
resources:
  requests:
    cpu: 250m
    memory: 256Mi
  limits:
    cpu: "1"
    memory: 512Mi
```

### No RBAC needed

The MCP gateway does not create Jobs or access the Kubernetes API.
No ServiceAccount RBAC beyond the default.

### Values

```yaml
mcpGateway:
  enabled: true
  replicaCount: 2
  port: 8080
  observePort: 9090
  resources:
    requests:
      cpu: 250m
      memory: 256Mi
    limits:
      cpu: "1"
      memory: 512Mi
```

## Files to create/modify

| File | Action |
|------|--------|
| `cmd/glyphoxa/main.go` | Add `runMCPGateway()`, add `"mcp-gateway"` to mode switch |
| `deploy/helm/glyphoxa/templates/mcp-gateway-deployment.yaml` | New |
| `deploy/helm/glyphoxa/templates/mcp-gateway-service.yaml` | New |
| `deploy/helm/glyphoxa/templates/networkpolicy.yaml` | Add MCP gateway policy |
| `deploy/helm/glyphoxa/templates/worker-job.yaml` | Add `GLYPHOXA_MCP_GATEWAY_URL` env var |
| `deploy/helm/glyphoxa/values.yaml` | Add `mcpGateway` section |

## Deferred

- **OPA/Kyverno admission policy** for Job image restriction: security
  hardening, not a functional requirement. Defer until a policy engine is
  chosen for the cluster.

## Acceptance criteria

- [x] `--mode=mcp-gateway` starts and serves MCP Streamable HTTP on `:8080`
- [x] Built-in stateless tools (dice, rules) are callable via the MCP protocol
- [x] External MCP servers from config are proxied through the gateway
- [x] Workers connect to the gateway via `GLYPHOXA_MCP_GATEWAY_URL` and can execute tools
- [x] Health probes (`/healthz`, `/readyz`) and metrics (`/metrics`) are live
- [x] Helm chart deploys MCP gateway with Service and NetworkPolicy
- [x] Memory tool remains on the worker and is not exposed via the gateway
