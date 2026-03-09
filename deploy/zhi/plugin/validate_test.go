package main

import (
	"context"
	"testing"

	"github.com/MrWong99/zhi/pkg/zhiplugin/config"
)

func TestValidateNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"valid namespace", "glyphoxa", false},
		{"valid with hyphens", "my-ns-123", false},
		{"uppercase blocks", "INVALID", true},
		{"spaces block", "a b", true},
		{"shell injection blocks", "ns;rm -rf /", true},
		{"empty blocks", "", true},
		{"starts with hyphen blocks", "-invalid", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateNamespace(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v (results: %v)", hasBlocking, tt.blocking, results)
			}
		})
	}
}

func TestValidateReleaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"valid name", "glyphoxa", false},
		{"valid with hyphens", "my-release-123", false},
		{"uppercase blocks", "INVALID", true},
		{"spaces block", "a b", true},
		{"empty blocks", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateReleaseName(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v", hasBlocking, tt.blocking)
			}
		})
	}
}

func TestValidateTopology(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"shared passes", "shared", false},
		{"dedicated passes", "dedicated", false},
		{"invalid blocks", "invalid", true},
		{"empty blocks", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateTopology(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v", hasBlocking, tt.blocking)
			}
		})
	}
}

func TestValidateAdminKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		val       any
		severity  config.Severity
		hasResult bool
	}{
		{"empty warns", "", config.Warning, true},
		{"valid key passes", "my-secure-key-123", 0, false},
		{"backtick blocks", "key`cmd`", config.Blocking, true},
		{"dollar blocks", "key$(cmd)", config.Blocking, true},
		{"double-quote blocks", `key"value`, config.Blocking, true},
		{"semicolon blocks", "key;rm", config.Blocking, true},
		{"newline blocks", "key\nvalue", config.Blocking, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateAdminKey(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.hasResult {
				if len(results) == 0 {
					t.Fatal("expected validation result, got none")
				}
				if results[0].Severity != tt.severity {
					t.Errorf("severity = %v, want %v", results[0].Severity, tt.severity)
				}
			} else if len(results) > 0 {
				t.Errorf("expected no results, got %v", results)
			}
		})
	}
}

func TestValidatePostgresPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"empty blocks", "", true},
		{"valid password passes", "secure-pass-123", false},
		{"newline blocks", "pass\nword", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validatePostgresPassword(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v", hasBlocking, tt.blocking)
			}
		})
	}
}

func TestValidateResourceProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"cloud passes", "cloud", false},
		{"whisper-native passes", "whisper-native", false},
		{"local-llm passes", "local-llm", false},
		{"invalid blocks", "custom", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateResourceProfile(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v", hasBlocking, tt.blocking)
			}
		})
	}
}

func TestValidateLogLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		val      any
		blocking bool
	}{
		{"debug passes", "debug", false},
		{"info passes", "info", false},
		{"warn passes", "warn", false},
		{"error passes", "error", false},
		{"trace blocks", "trace", true},
		{"empty blocks", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateLogLevel(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasBlocking := len(results) > 0 && results[0].Severity == config.Blocking
			if hasBlocking != tt.blocking {
				t.Errorf("blocking = %v, want %v", hasBlocking, tt.blocking)
			}
		})
	}
}

func TestValidateYAML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		val       any
		hasResult bool
	}{
		{"empty passes", "", false},
		{"valid yaml passes", "key: value\nlist:\n  - item1", false},
		{"invalid yaml warns", "key: [invalid", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := validateYAML(config.Value{Val: tt.val}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.hasResult && len(results) == 0 {
				t.Error("expected validation result, got none")
			}
			if !tt.hasResult && len(results) > 0 {
				t.Errorf("expected no results, got %v", results)
			}
		})
	}
}

func TestValidateReplicaCountDedicated(t *testing.T) {
	t.Parallel()
	tree := config.NewTree()
	if err := tree.Set("core/topology", &config.Value{Val: "dedicated"}); err != nil {
		t.Fatal(err)
	}
	if err := tree.Set("gateway/replica-count", &config.Value{Val: 5}); err != nil {
		t.Fatal(err)
	}

	results, err := validateReplicaCountDedicated(config.Value{Val: 5}, tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected warning for dedicated topology with 5 replicas")
	}
	if results[0].Severity != config.Warning {
		t.Errorf("severity = %v, want Warning", results[0].Severity)
	}
}

func TestValidateReplicaCountShared(t *testing.T) {
	t.Parallel()
	tree := config.NewTree()
	if err := tree.Set("core/topology", &config.Value{Val: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := tree.Set("gateway/replica-count", &config.Value{Val: 5}); err != nil {
		t.Fatal(err)
	}

	results, err := validateReplicaCountDedicated(config.Value{Val: 5}, tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 0 {
		t.Errorf("expected no results for shared topology, got %v", results)
	}
}

func TestValidateGPUEnabled(t *testing.T) {
	t.Parallel()
	tree := config.NewTree()
	if err := tree.Set("worker/resource-profile", &config.Value{Val: "local-llm"}); err != nil {
		t.Fatal(err)
	}
	if err := tree.Set("worker/gpu-enabled", &config.Value{Val: false}); err != nil {
		t.Fatal(err)
	}

	results, err := validateGPUEnabled(config.Value{Val: false}, tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected warning for local-llm with GPU disabled")
	}
	if results[0].Severity != config.Warning {
		t.Errorf("severity = %v, want Warning", results[0].Severity)
	}
}

func TestValidatorsMapOnlyReferencesKnownPaths(t *testing.T) {
	t.Parallel()
	known := make(map[string]bool, len(valueDefs))
	for _, d := range valueDefs {
		known[d.Path] = true
	}
	for path := range validators {
		if !known[path] {
			t.Errorf("validator registered for unknown path: %s", path)
		}
	}
}

func TestPluginValidateDispatch(t *testing.T) {
	t.Parallel()
	p := newGlyphoxaPlugin()

	// Build a tree reader from the plugin's own defaults.
	tree := config.NewTree()
	paths, _ := p.List(context.Background())
	for _, path := range paths {
		v, ok, _ := p.Get(context.Background(), path)
		if ok {
			if err := tree.Set(path, &v); err != nil {
				t.Fatal(err)
			}
		}
	}

	// postgresql/postgres-password defaults to "" which is required -- should block.
	results, err := p.Validate(context.Background(), "postgresql/postgres-password", tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected blocking result for empty postgresql/postgres-password")
	}
	if results[0].Severity != config.Blocking {
		t.Errorf("severity = %v, want Blocking", results[0].Severity)
	}

	// core/image-tag has no validator -- should return nil.
	results, err = p.Validate(context.Background(), "core/image-tag", tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for core/image-tag, got %v", results)
	}
}
