package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/MrWong99/zhi/pkg/zhiplugin/config"

	"gopkg.in/yaml.v3"
)

// validatorFunc validates a single value, optionally using the full tree
// for cross-value checks.
type validatorFunc func(v config.Value, tree config.TreeReader) ([]config.ValidationResult, error)

// rfc1123 matches valid RFC 1123 DNS labels.
var rfc1123 = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// shellMetachars are characters that must not appear in values interpolated
// into shell scripts.
var shellMetachars = regexp.MustCompile("[\"`;$\n]")

// validators maps config paths to their validation functions.
// Only paths that need validation are listed -- unlisted paths are always valid.
var validators = map[string]validatorFunc{
	"core/namespace":                      validateNamespace,
	"core/release-name":                   validateReleaseName,
	"core/topology":                       validateTopology,
	"database/dsn":                        validateDSN,
	"gateway/replica-count":               validateReplicaCountDedicated,
	"gateway/pod-anti-affinity":           validateAntiAffinityDedicated,
	"gateway/admin-key":                   validateAdminKey,
	"worker/resource-profile":             validateResourceProfile,
	"worker/gpu-enabled":                  validateGPUEnabled,
	"postgresql/postgres-password":        validatePostgresPassword,
	"config/server/log-level":             validateLogLevel,
	"gateway/tolerations":                 validateYAML,
	"gateway/node-selector":               validateYAML,
	"worker/tolerations":                  validateYAML,
	"worker/node-selector":                validateYAML,
	"mcp-gateway/tolerations":             validateYAML,
	"mcp-gateway/node-selector":           validateYAML,
	"gateway/service-account-annotations": validateYAML,
	"config/mcp-servers":                  validateYAML,
}

func init() {
	// Register YAML validators for all provider options paths.
	for _, p := range []string{"llm", "stt", "tts", "s2s", "embeddings", "vad", "audio"} {
		validators["config/providers/"+p+"/options"] = validateYAML
	}
}

func validateNamespace(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if !rfc1123.MatchString(s) {
		return []config.ValidationResult{{
			Message:  "Namespace must be a valid RFC 1123 DNS label (lowercase alphanumeric and hyphens, 1-63 chars, must start with alphanumeric)",
			Severity: config.Blocking,
		}}, nil
	}
	return nil, nil
}

func validateReleaseName(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if !rfc1123.MatchString(s) {
		return []config.ValidationResult{{
			Message:  "Release name must be a valid RFC 1123 DNS label (lowercase alphanumeric and hyphens, 1-63 chars, must start with alphanumeric)",
			Severity: config.Blocking,
		}}, nil
	}
	return nil, nil
}

func validateTopology(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if s != "shared" && s != "dedicated" {
		return []config.ValidationResult{{
			Message:  fmt.Sprintf("Topology must be 'shared' or 'dedicated', got %q", s),
			Severity: config.Blocking,
		}}, nil
	}
	return nil, nil
}

func validateDSN(v config.Value, tree config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	// Only block if postgresql component is disabled AND dsn is empty.
	// We check for a postgresql path as a proxy for the component being enabled.
	pgPass, pgFound := tree.Get("postgresql/postgres-password")
	pgPassStr, _ := pgPass.Val.(string)
	// If postgresql is not configured (no password set) and DSN is empty, block.
	if s == "" && (!pgFound || pgPassStr == "") {
		return []config.ValidationResult{{
			Message:  "Database DSN is required when the postgresql component is disabled. Either enable the postgresql component or provide a DSN.",
			Severity: config.Warning,
		}}, nil
	}
	return nil, nil
}

func validateReplicaCountDedicated(v config.Value, tree config.TreeReader) ([]config.ValidationResult, error) {
	topo, _ := tree.Get("core/topology")
	topoStr, _ := topo.Val.(string)
	if topoStr != "dedicated" {
		return nil, nil
	}
	count := toInt(v.Val)
	if count > 2 {
		return []config.ValidationResult{{
			Message:  fmt.Sprintf("Dedicated topology typically uses 1-2 replicas, but %d are configured", count),
			Severity: config.Warning,
		}}, nil
	}
	return nil, nil
}

func validateAntiAffinityDedicated(v config.Value, tree config.TreeReader) ([]config.ValidationResult, error) {
	topo, _ := tree.Get("core/topology")
	topoStr, _ := topo.Val.(string)
	if topoStr != "dedicated" {
		return nil, nil
	}
	enabled, _ := v.Val.(bool)
	if enabled {
		return []config.ValidationResult{{
			Message:  "Pod anti-affinity is enabled with dedicated topology. This is usually unnecessary for single-tenant deployments.",
			Severity: config.Warning,
		}}, nil
	}
	return nil, nil
}

func validateAdminKey(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if s == "" {
		return []config.ValidationResult{{
			Message:  "Admin key is empty. The admin API will be unauthenticated.",
			Severity: config.Warning,
		}}, nil
	}
	if shellMetachars.MatchString(s) {
		return []config.ValidationResult{{
			Message:  "Admin key contains shell metacharacters (quotes, backticks, $, semicolons, or newlines) which are not allowed",
			Severity: config.Blocking,
		}}, nil
	}
	return nil, nil
}

func validateResourceProfile(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	switch s {
	case "cloud", "whisper-native", "local-llm":
		return nil, nil
	default:
		return []config.ValidationResult{{
			Message:  fmt.Sprintf("Resource profile must be 'cloud', 'whisper-native', or 'local-llm', got %q", s),
			Severity: config.Blocking,
		}}, nil
	}
}

func validateGPUEnabled(v config.Value, tree config.TreeReader) ([]config.ValidationResult, error) {
	profile, _ := tree.Get("worker/resource-profile")
	profileStr, _ := profile.Val.(string)
	if profileStr != "local-llm" {
		return nil, nil
	}
	enabled, _ := v.Val.(bool)
	if !enabled {
		return []config.ValidationResult{{
			Message:  "GPU scheduling is disabled but resource profile is 'local-llm'. The worker may not have access to GPU resources.",
			Severity: config.Warning,
		}}, nil
	}
	return nil, nil
}

func validatePostgresPassword(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if s == "" {
		return []config.ValidationResult{{
			Message:  "PostgreSQL password is required when the postgresql component is enabled",
			Severity: config.Blocking,
		}}, nil
	}
	if strings.Contains(s, "\n") {
		return []config.ValidationResult{{
			Message:  "PostgreSQL password must not contain newlines",
			Severity: config.Blocking,
		}}, nil
	}
	return nil, nil
}

func validateLogLevel(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	switch s {
	case "debug", "info", "warn", "error":
		return nil, nil
	default:
		return []config.ValidationResult{{
			Message:  fmt.Sprintf("Log level must be 'debug', 'info', 'warn', or 'error', got %q", s),
			Severity: config.Blocking,
		}}, nil
	}
}

func validateYAML(v config.Value, _ config.TreeReader) ([]config.ValidationResult, error) {
	s, _ := v.Val.(string)
	if s == "" {
		return nil, nil
	}
	var out any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		return []config.ValidationResult{{
			Message:  fmt.Sprintf("Invalid YAML: %v", err),
			Severity: config.Warning,
		}}, nil
	}
	return nil, nil
}

// toInt converts a value to int, handling float64 (from JSON) and int.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
