package llmbench

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

// ValidateInput checks a tool call's arguments against the Tool's declared
// input schema. It covers exactly the JSON Schema subset the built-in Tools
// declare — a top-level object with typed properties, `required`, `enum`,
// `minimum`/`maximum`, and `additionalProperties: false` — which is what
// "the model produced a schema-valid call" means for this bench. It is not a
// general validator.
func ValidateInput(schema, input json.RawMessage) error {
	var s struct {
		Properties map[string]struct {
			Type    string   `json:"type"`
			Enum    []any    `json:"enum"`
			Minimum *float64 `json:"minimum"`
			Maximum *float64 `json:"maximum"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("schema is not valid JSON: %w", err)
	}

	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	args, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("arguments are not a JSON object")
	}

	for _, name := range s.Required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("required property %q is missing", name)
		}
	}
	for name, v := range args {
		p, known := s.Properties[name]
		if !known {
			if s.AdditionalProperties != nil && !*s.AdditionalProperties {
				return fmt.Errorf("unknown property %q", name)
			}
			continue
		}
		num, isNum := v.(float64)
		switch p.Type {
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("property %q is not a string", name)
			}
		case "integer":
			if !isNum || num != math.Trunc(num) {
				return fmt.Errorf("property %q is not an integer", name)
			}
		case "number":
			if !isNum {
				return fmt.Errorf("property %q is not a number", name)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("property %q is not a boolean", name)
			}
		}
		if isNum {
			if p.Minimum != nil && num < *p.Minimum {
				return fmt.Errorf("property %q is below its minimum", name)
			}
			if p.Maximum != nil && num > *p.Maximum {
				return fmt.Errorf("property %q is above its maximum", name)
			}
		}
		if len(p.Enum) > 0 && !slices.Contains(p.Enum, v) {
			return fmt.Errorf("property %q is not one of its enum values", name)
		}
	}
	return nil
}
