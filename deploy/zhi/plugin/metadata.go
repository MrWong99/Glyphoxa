package main

import "github.com/MrWong99/zhi/pkg/zhiplugin/config"

// ValueDef defines a configuration value with its default and metadata.
// This reduces the boilerplate of repeating the same metadata label keys
// across all config values.
type ValueDef struct {
	Path        string // slash-delimited config path, e.g. "core/namespace"
	Default     any    // default value
	Section     string // ui.section
	DisplayName string // ui.displayName
	Description string // core.description
	Type        string // core.type (string, int, bool, map, yaml, list)

	// Optional fields -- zero values mean "not set"
	Placeholder string   // ui.placeholder
	Password    bool     // ui.password
	Required    bool     // config.required
	SelectFrom  []string // ui.enum (dropdown selection)
}

// ToValue converts a ValueDef to a config.Value with the standard
// metadata labels populated.
func (d *ValueDef) ToValue() *config.Value {
	md := map[string]any{
		"ui.section":       d.Section,
		"ui.displayName":   d.DisplayName,
		"core.description": d.Description,
		"core.type":        d.Type,
	}
	if d.Placeholder != "" {
		md["ui.placeholder"] = d.Placeholder
	}
	if d.Password {
		md["ui.password"] = true
	}
	if d.Required {
		md["config.required"] = true
	}
	if len(d.SelectFrom) > 0 {
		md["ui.enum"] = d.SelectFrom
	}
	return &config.Value{
		Val:      d.Default,
		Metadata: md,
	}
}

// providerDefs generates 5 ValueDef entries for a provider section.
// Each provider has: name, api-key, base-url, model, options.
func providerDefs(prefix, section, displayPrefix string) []ValueDef {
	return []ValueDef{
		{
			Path: prefix + "/name", Section: section,
			DisplayName: displayPrefix + " Provider",
			Description: "Registered provider name (e.g., openai, deepgram)",
			Type: "string", Default: "",
		},
		{
			Path: prefix + "/api-key", Section: section,
			DisplayName: displayPrefix + " API Key",
			Description: "Authentication key for the provider API",
			Type: "string", Default: "", Password: true,
		},
		{
			Path: prefix + "/base-url", Section: section,
			DisplayName: displayPrefix + " Base URL",
			Description: "Override the provider's default API endpoint",
			Type: "string", Default: "",
		},
		{
			Path: prefix + "/model", Section: section,
			DisplayName: displayPrefix + " Model",
			Description: "Model selection (e.g., gpt-4o, nova-2)",
			Type: "string", Default: "",
		},
		{
			Path: prefix + "/options", Section: section,
			DisplayName: displayPrefix + " Options",
			Description: "Provider-specific options as YAML",
			Type: "string", Default: "",
			Placeholder: "temperature: 0.7\ntop_p: 0.9",
		},
	}
}
