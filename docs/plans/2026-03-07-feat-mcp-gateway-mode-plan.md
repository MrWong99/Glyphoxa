# MCP Gateway Mode Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `--mode=mcp-gateway` to serve stateless MCP tools over Streamable HTTP, so workers share a single tool pool instead of spawning duplicate MCP server subprocesses per session.

**Architecture:** The MCP gateway creates an `mcphost.Host`, registers all stateless built-in tools (dice, rules) and external MCP servers from config, then wraps them in an `mcp.Server` exposed via `mcp.NewStreamableHTTPHandler`. Workers connect to it via `StreamableClientTransport` using the existing `RegisterServer` flow.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk/mcp` v1.4.0 (MCP server + Streamable HTTP handler), existing `internal/mcp/mcphost` package, Helm 3.

**Design doc:** `docs/plans/2026-03-07-feat-mcp-gateway-mode-design.md`

---

### Task 1: Register stateless built-in tools in a reusable helper

Currently `diceroller.Tools()` and `ruleslookup.Tools()` exist but are never registered. Extract a helper function that registers all stateless built-in tools on an `mcphost.Host`, usable by both `runFull` and `runMCPGateway`.

**Files:**
- Create: `internal/mcp/mcphost/register_stateless.go`
- Test: `internal/mcp/mcphost/register_stateless_test.go`

**Step 1: Write the failing test**

```go
// internal/mcp/mcphost/register_stateless_test.go
package mcphost

import (
	"testing"

	"github.com/MrWong99/glyphoxa/internal/mcp"
)

func TestRegisterStatelessTools(t *testing.T) {
	t.Parallel()

	h := New()
	defer h.Close()

	if err := RegisterStatelessTools(h); err != nil {
		t.Fatalf("RegisterStatelessTools: %v", err)
	}

	// Should have dice (roll, roll_table) and rules (search_rules, get_rule) tools.
	tools := h.AvailableTools(mcp.BudgetDeep)
	if len(tools) < 4 {
		t.Errorf("expected at least 4 tools, got %d", len(tools))
	}

	// Verify specific tools exist.
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"roll", "roll_table", "search_rules", "get_rule"} {
		if !names[want] {
			t.Errorf("missing expected tool %q", want)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -count=1 -run TestRegisterStatelessTools ./internal/mcp/mcphost/...`
Expected: FAIL — `RegisterStatelessTools` undefined.

**Step 3: Write the implementation**

```go
// internal/mcp/mcphost/register_stateless.go
package mcphost

import (
	"fmt"

	"github.com/MrWong99/glyphoxa/internal/mcp/tools"
	"github.com/MrWong99/glyphoxa/internal/mcp/tools/diceroller"
	"github.com/MrWong99/glyphoxa/internal/mcp/tools/ruleslookup"
)

// RegisterStatelessTools registers all built-in stateless tools (dice roller,
// rules lookup) on the given Host. These tools have no external dependencies
// and are safe to share across tenants.
//
// Tenant-scoped tools (memory, fileio) are NOT included here; they require
// per-session state and must be registered by the worker.
func RegisterStatelessTools(h *Host) error {
	allTools := make([]tools.Tool, 0, 8)
	allTools = append(allTools, diceroller.Tools()...)
	allTools = append(allTools, ruleslookup.Tools()...)

	for _, t := range allTools {
		if err := h.RegisterBuiltin(BuiltinTool{
			Definition:  t.Definition,
			Handler:     t.Handler,
			DeclaredP50: t.DeclaredP50,
			DeclaredMax: t.DeclaredMax,
		}); err != nil {
			return fmt.Errorf("register stateless tool %q: %w", t.Definition.Name, err)
		}
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -count=1 -run TestRegisterStatelessTools ./internal/mcp/mcphost/...`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/mcp/mcphost/register_stateless.go internal/mcp/mcphost/register_stateless_test.go
git commit -m "feat(mcp): add RegisterStatelessTools helper for shared tool registration"
```

---

### Task 2: Implement `runMCPGateway` in main.go

Add the `--mode=mcp-gateway` case and `runMCPGateway` function. This function:
1. Creates an `mcphost.Host` with stateless built-in tools + external MCP servers from config
2. Calibrates tool latencies
3. Creates an `mcp.Server` (Go SDK) and registers each tool from the host
4. Serves via `mcp.NewStreamableHTTPHandler` on a configurable port
5. Starts the observe server for health/metrics
6. Handles graceful shutdown

**Files:**
- Modify: `cmd/glyphoxa/main.go:114` (mode switch) and add `runMCPGateway` function

**Step 1: Add `"mcp-gateway"` to the mode switch**

In `cmd/glyphoxa/main.go`, find the switch statement at line 114:

```go
// Change:
	default:
		fmt.Fprintf(os.Stderr, "glyphoxa: unknown mode %q (valid: full, gateway, worker)\n", *mode)

// To:
	case "mcp-gateway":
		return runMCPGateway(cfg)
	default:
		fmt.Fprintf(os.Stderr, "glyphoxa: unknown mode %q (valid: full, gateway, worker, mcp-gateway)\n", *mode)
```

**Step 2: Add `runMCPGateway` function**

Add after `runWorker` in `cmd/glyphoxa/main.go`. The function needs these imports added:
- `mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"`
- `"github.com/MrWong99/glyphoxa/internal/mcp/mcphost"`

```go
// runMCPGateway runs the MCP gateway mode: a shared MCP server over Streamable
// HTTP that hosts stateless tools (dice, rules, external MCP servers) for all
// worker pods.
func runMCPGateway(cfg *config.Config) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	printStartupSummary(cfg, "mcp-gateway")

	// ── MCP Host with stateless tools ────────────────────────────────────────
	host := mcphost.New()

	if err := mcphost.RegisterStatelessTools(host); err != nil {
		slog.Error("failed to register stateless tools", "err", err)
		return 1
	}

	// Register external MCP servers from config.
	for _, srv := range cfg.MCP.Servers {
		serverCfg := mcp.ServerConfig{
			Name:      srv.Name,
			Transport: srv.Transport,
			Command:   srv.Command,
			URL:       srv.URL,
			Env:       srv.Env,
		}
		if err := host.RegisterServer(ctx, serverCfg); err != nil {
			slog.Error("failed to register MCP server", "name", srv.Name, "err", err)
			return 1
		}
		slog.Info("registered MCP server", "name", srv.Name)
	}

	if err := host.Calibrate(ctx); err != nil {
		slog.Warn("MCP calibration failed, using declared latencies", "err", err)
	}

	// ── MCP SDK Server ───────────────────────────────────────────────────────
	mcpServer := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "glyphoxa-mcp-gateway", Version: "1.0.0"},
		nil,
	)

	// Register each tool from the host on the MCP SDK server.
	allTools := host.AvailableTools(mcp.BudgetDeep)
	for _, toolDef := range allTools {
		toolName := toolDef.Name
		mcpTool := &mcpsdk.Tool{
			Name:        toolDef.Name,
			Description: toolDef.Description,
		}
		if toolDef.Parameters != nil {
			mcpTool.InputSchema = mcpsdk.NewToolInputSchema(toolDef.Parameters)
		}

		mcpsdk.AddTool(mcpServer, mcpTool, func(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			argsJSON := "{}"
			if req.Arguments != nil {
				data, err := json.Marshal(req.Arguments)
				if err != nil {
					return nil, nil, fmt.Errorf("mcp-gateway: failed to marshal tool args: %w", err)
				}
				argsJSON = string(data)
			}

			result, err := host.ExecuteTool(ctx, toolName, argsJSON)
			if err != nil {
				return nil, nil, err
			}

			callResult := &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Content}},
				IsError: result.IsError,
			}
			return callResult, nil, nil
		})
	}

	slog.Info("registered MCP tools on gateway", "count", len(allTools))

	// ── HTTP Server (MCP Streamable HTTP) ────────────────────────────────────
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		return mcpServer
	}, nil)

	mcpAddr := os.Getenv("GLYPHOXA_MCP_ADDR")
	if mcpAddr == "" {
		mcpAddr = ":8080"
	}

	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)

	mcpSrv := &http.Server{
		Addr:    mcpAddr,
		Handler: mcpMux,
	}
	go func() {
		ln, err := net.Listen("tcp", mcpAddr)
		if err != nil {
			slog.Error("MCP HTTP listen failed", "addr", mcpAddr, "err", err)
			return
		}
		slog.Info("MCP gateway started", "addr", ln.Addr().String())
		if err := mcpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("MCP HTTP server error", "err", err)
		}
	}()

	// ── Observability ────────────────────────────────────────────────────────
	observeSrv := startObserveServer(cfg)

	slog.Info("mcp-gateway ready — press Ctrl+C to shut down",
		"mode", "mcp-gateway",
		"mcp_addr", mcpAddr,
	)

	<-ctx.Done()

	// ── Graceful shutdown ────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	if err := mcpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("MCP HTTP server shutdown error", "err", err)
	}
	if err := observeSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("observe server shutdown error", "err", err)
	}
	if err := host.Close(); err != nil {
		slog.Warn("MCP host close error", "err", err)
	}

	slog.Info("goodbye")
	return 0
}
```

Note: `encoding/json` is needed in the imports (already present). `mcpsdk` alias for `github.com/modelcontextprotocol/go-sdk/mcp` needs to be added to the import block.

**Step 3: Verify it compiles**

Run: `go build ./cmd/glyphoxa`
Expected: Success

**Step 4: Verify the mode dispatches correctly**

Run: `go run ./cmd/glyphoxa --mode=mcp-gateway --config=configs/example.yaml 2>&1 | head -5`
Expected: Startup log showing `mode=mcp-gateway`. It will likely fail to bind ports or register external servers (depending on config), but the mode dispatch must work. Ctrl+C to stop.

**Step 5: Commit**

```bash
git add cmd/glyphoxa/main.go
git commit -m "feat(mcp-gateway): add --mode=mcp-gateway with Streamable HTTP server"
```

---

### Task 3: Wire `GLYPHOXA_MCP_GATEWAY_URL` in worker mode

When the env var is set, the worker auto-registers the MCP gateway as a remote server on its local `mcphost.Host`.

**Files:**
- Modify: `cmd/glyphoxa/main.go` (`runWorker` function, around line 370)

**Step 1: Add MCP gateway registration to runWorker**

Currently `runWorker` does not create an `mcphost.Host`. The MCP host is created inside `app.New()` → `initMCP()`. The cleanest integration point is to check `GLYPHOXA_MCP_GATEWAY_URL` inside `initMCP` and register it as a server there.

Modify `internal/app/app.go` in `initMCP()` (around line 250). After the external server registration loop (line 268) and before Calibrate (line 270), add:

```go
	// Auto-register shared MCP gateway if URL is provided.
	if mcpGatewayURL := os.Getenv("GLYPHOXA_MCP_GATEWAY_URL"); mcpGatewayURL != "" {
		gwCfg := mcp.ServerConfig{
			Name:      "mcp-gateway",
			Transport: mcp.TransportStreamableHTTP,
			URL:       mcpGatewayURL,
		}
		if err := a.mcpHost.RegisterServer(ctx, gwCfg); err != nil {
			return fmt.Errorf("register mcp-gateway at %s: %w", mcpGatewayURL, err)
		}
		slog.Info("registered shared MCP gateway", "url", mcpGatewayURL)
	}
```

Add `"os"` to the import block of `internal/app/app.go` if not already present.

**Step 2: Verify it compiles**

Run: `go build ./cmd/glyphoxa`
Expected: Success

**Step 3: Run existing tests to verify no regressions**

Run: `go test -race -count=1 ./internal/app/...`
Expected: All tests pass. The env var is not set in tests, so the new code path is skipped.

**Step 4: Commit**

```bash
git add internal/app/app.go
git commit -m "feat(worker): auto-register MCP gateway from GLYPHOXA_MCP_GATEWAY_URL"
```

---

### Task 4: Helm chart — MCP gateway Deployment and Service

**Files:**
- Create: `deploy/helm/glyphoxa/templates/mcp-gateway-deployment.yaml`
- Create: `deploy/helm/glyphoxa/templates/mcp-gateway-service.yaml`
- Modify: `deploy/helm/glyphoxa/values.yaml` (add `mcpGateway` section)
- Modify: `deploy/helm/glyphoxa/templates/_helpers.tpl` (add MCP gateway selector labels)

**Step 1: Add MCP gateway helpers to `_helpers.tpl`**

Append to `deploy/helm/glyphoxa/templates/_helpers.tpl`:

```
{{/*
Selector labels for MCP gateway.
*/}}
{{- define "glyphoxa.mcpGateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "glyphoxa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: mcp-gateway
{{- end }}
```

**Step 2: Add `mcpGateway` section to `values.yaml`**

Add before the `postgresql` section in `deploy/helm/glyphoxa/values.yaml`:

```yaml
# -- MCP Gateway configuration (--mode=mcp-gateway)
# Shared MCP tool server for all workers. Hosts stateless tools (dice, rules)
# and proxies external MCP servers.
mcpGateway:
  enabled: true
  replicaCount: 2

  ports:
    mcp: 8080
    observe: 9090

  resources:
    requests:
      cpu: 250m
      memory: 256Mi
    limits:
      cpu: "1"
      memory: 512Mi

  nodeSelector: {}
  tolerations: []
```

**Step 3: Create MCP gateway Deployment**

Create `deploy/helm/glyphoxa/templates/mcp-gateway-deployment.yaml`:

```yaml
{{- if .Values.mcpGateway.enabled }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "glyphoxa.fullname" . }}-mcp-gateway
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "glyphoxa.labels" . | nindent 4 }}
    {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 4 }}
spec:
  replicas: {{ .Values.mcpGateway.replicaCount }}
  selector:
    matchLabels:
      {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "glyphoxa.labels" . | nindent 8 }}
        {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 8 }}
      annotations:
        checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.mcpGateway.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.mcpGateway.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: mcp-gateway
          image: {{ include "glyphoxa.image" . }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          args: ["--mode=mcp-gateway"]
          ports:
            - name: mcp
              containerPort: {{ .Values.mcpGateway.ports.mcp }}
              protocol: TCP
            - name: observe
              containerPort: {{ .Values.mcpGateway.ports.observe }}
              protocol: TCP
          env:
            - name: GLYPHOXA_MCP_ADDR
              value: ":{{ .Values.mcpGateway.ports.mcp }}"
          {{- if .Values.config.data }}
          volumeMounts:
            - name: config
              mountPath: /etc/glyphoxa
              readOnly: true
          {{- end }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: observe
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /readyz
              port: observe
            initialDelaySeconds: 5
            periodSeconds: 10
          startupProbe:
            httpGet:
              path: /healthz
              port: observe
            failureThreshold: 30
            periodSeconds: 2
          resources:
            {{- toYaml .Values.mcpGateway.resources | nindent 12 }}
      {{- if .Values.config.data }}
      volumes:
        - name: config
          configMap:
            name: {{ include "glyphoxa.fullname" . }}-config
      {{- end }}
{{- end }}
```

**Step 4: Create MCP gateway Service**

Create `deploy/helm/glyphoxa/templates/mcp-gateway-service.yaml`:

```yaml
{{- if .Values.mcpGateway.enabled }}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "glyphoxa.fullname" . }}-mcp-gateway
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "glyphoxa.labels" . | nindent 4 }}
    {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - name: mcp
      port: {{ .Values.mcpGateway.ports.mcp }}
      targetPort: mcp
      protocol: TCP
    - name: observe
      port: {{ .Values.mcpGateway.ports.observe }}
      targetPort: observe
      protocol: TCP
  selector:
    {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 4 }}
{{- end }}
```

**Step 5: Commit**

```bash
git add deploy/helm/glyphoxa/templates/_helpers.tpl \
  deploy/helm/glyphoxa/templates/mcp-gateway-deployment.yaml \
  deploy/helm/glyphoxa/templates/mcp-gateway-service.yaml \
  deploy/helm/glyphoxa/values.yaml
git commit -m "feat(helm): add MCP gateway Deployment and Service"
```

---

### Task 5: Helm chart — NetworkPolicy and worker Job template update

**Files:**
- Modify: `deploy/helm/glyphoxa/templates/networkpolicy.yaml` (add MCP gateway policy)
- Modify: `deploy/helm/glyphoxa/templates/worker-job.yaml` (add `GLYPHOXA_MCP_GATEWAY_URL` env var)

**Step 1: Add MCP gateway NetworkPolicy**

Append to `deploy/helm/glyphoxa/templates/networkpolicy.yaml`, before the final `{{- end }}`:

```yaml
---
# MCP Gateway: can receive MCP HTTP from workers; can reach external APIs and DNS.
# Cannot reach PostgreSQL (no tenant data).
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "glyphoxa.fullname" . }}-mcp-gateway
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "glyphoxa.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "glyphoxa.mcpGateway.selectorLabels" . | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    # Allow MCP HTTP from workers.
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/component: worker
      ports:
        - port: {{ .Values.mcpGateway.ports.mcp }}
          protocol: TCP
    # Allow observe (metrics scraping) from any namespace (Prometheus).
    - ports:
        - port: {{ .Values.mcpGateway.ports.observe }}
          protocol: TCP
  egress:
    # DNS.
    - ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
    # External APIs (MCP servers over HTTPS).
    - ports:
        - port: 443
          protocol: TCP
```

Wrap the new block in `{{- if .Values.mcpGateway.enabled }}` / `{{- end }}`.

**Step 2: Add `GLYPHOXA_MCP_GATEWAY_URL` to worker Job template**

In `deploy/helm/glyphoxa/templates/worker-job.yaml`, add to the worker container's `env` list (after the `GLYPHOXA_GRPC_ADDR` entry):

```yaml
                {{- if .Values.mcpGateway.enabled }}
                - name: GLYPHOXA_MCP_GATEWAY_URL
                  value: "http://{{ include "glyphoxa.fullname" . }}-mcp-gateway.{{ .Release.Namespace }}.svc:{{ .Values.mcpGateway.ports.mcp }}/mcp"
                {{- end }}
```

**Step 3: Also update worker NetworkPolicy egress**

In the existing worker NetworkPolicy egress rules, add a rule allowing workers to reach the MCP gateway:

```yaml
    # MCP Gateway.
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/component: mcp-gateway
      ports:
        - port: {{ .Values.mcpGateway.ports.mcp }}
          protocol: TCP
```

Add this after the gateway gRPC egress rule in the worker NetworkPolicy section.

**Step 4: Commit**

```bash
git add deploy/helm/glyphoxa/templates/networkpolicy.yaml \
  deploy/helm/glyphoxa/templates/worker-job.yaml
git commit -m "feat(helm): add MCP gateway NetworkPolicy and worker env var"
```

---

### Task 6: Update plan and run full checks

**Files:**
- Modify: `docs/plans/2026-03-05-feat-production-scaling-multi-tenant-deployment-plan.md`
- Modify: `docs/plans/2026-03-07-feat-mcp-gateway-mode-design.md`

**Step 1: Run full test suite**

Run: `go test -race -count=1 ./...`
Expected: All tests pass.

**Step 2: Run vet and fmt**

Run: `go vet ./... && gofmt -l .`
Expected: No output (all clean).

**Step 3: Mark acceptance criteria in design doc**

Check off completed items in `docs/plans/2026-03-07-feat-mcp-gateway-mode-design.md`:
- `[x]` for each acceptance criterion that is met.

**Step 4: Update Phase 3b in the main plan**

In `docs/plans/2026-03-05-feat-production-scaling-multi-tenant-deployment-plan.md`, check off the MCP gateway acceptance criteria under Phase 3b.

**Step 5: Commit**

```bash
git add docs/plans/2026-03-05-feat-production-scaling-multi-tenant-deployment-plan.md \
  docs/plans/2026-03-07-feat-mcp-gateway-mode-design.md
git commit -m "docs: mark MCP gateway implementation complete (Phase 3b.1)"
```

---

## Task Dependencies

```
Task 1 (RegisterStatelessTools helper)
  └──> Task 2 (runMCPGateway — uses the helper)
       └──> Task 3 (worker GLYPHOXA_MCP_GATEWAY_URL — depends on gateway existing)
Task 4 (Helm Deployment + Service — independent of Go code)
  └──> Task 5 (NetworkPolicy + worker Job update — depends on Task 4 values)
Task 6 (final checks — depends on all above)
```

Tasks 1→2→3 are sequential (Go code). Tasks 4→5 are sequential (Helm). These two chains are independent and can be parallelized.
