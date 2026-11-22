package llmbench

import (
	"fmt"
	"os"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// Candidate is one arm of the comparison: a provider client, the model it is
// asked for, and how the result is labelled and priced.
type Candidate struct {
	// Name is the stable id used in the candidate selection, file names and the
	// comparison table.
	Name string
	// Provider is the price/metrics label of the provider (internal/observe).
	Provider observe.Provider
	// Model is the model id every request of this arm names; it is also the
	// price-table key.
	Model string
	// Settings describes the client options in a few words, for the table.
	Settings string
	// KeyEnv is the environment variable the provider client reads its
	// credential from.
	KeyEnv string
	// Optional arms run only when named (or with the selection "all").
	Optional bool
	// New builds the provider client. It is called once per script, so every
	// script starts from a client with no state.
	New func() llm.Provider
}

// thinkingOff is the bench-only policy for the thinking-disabled arm.
func thinkingOff(string) anthropic.Thinking {
	return anthropic.Thinking{Mode: anthropic.ThinkingDisabled}
}

// Candidates is the candidate table of #702. Every arm goes through the
// production adapter with the options production would use; only the
// thinking-disabled arm overrides the adapter's policy.
func Candidates() []Candidate {
	return []Candidate{
		{
			Name: "groq-gpt-oss-120b", Provider: observe.ProviderGroq, Model: "openai/gpt-oss-120b",
			Settings: "reasoning_effort low (baseline, production default)", KeyEnv: groq.APIKeyEnv,
			New: func() llm.Provider { return groq.New("") },
		},
		{
			Name: "haiku-5-5-adaptive-low", Provider: observe.ProviderAnthropic, Model: "claude-haiku-5-5",
			Settings: "thinking adaptive, effort low (adapter default)", KeyEnv: anthropic.APIKeyEnv,
			New: func() llm.Provider { return anthropic.New("") },
		},
		{
			Name: "haiku-5-5-thinking-off", Provider: observe.ProviderAnthropic, Model: "claude-haiku-5-5",
			Settings: "thinking disabled", KeyEnv: anthropic.APIKeyEnv,
			New: func() llm.Provider { return anthropic.New("", anthropic.WithThinkingFor(thinkingOff)) },
		},
		{
			Name: "haiku-4-5", Provider: observe.ProviderAnthropic, Model: "claude-haiku-4-5",
			Settings: "no thinking fields", KeyEnv: anthropic.APIKeyEnv,
			New: func() llm.Provider { return anthropic.New("") },
		},
		{
			Name: "groq-qwen3.8-27b", Provider: observe.ProviderGroq, Model: "qwen/qwen3.8-27b",
			Settings: "no reasoning field (as the groq preset sends it)", KeyEnv: groq.APIKeyEnv,
			Optional: true,
			New:      func() llm.Provider { return groq.New("") },
		},
	}
}

// SelectCandidates picks the arms to run from all. spec is a comma-separated
// list of names; "" selects every non-optional arm and "all" every arm. An
// unknown name is an error naming the valid ones.
func SelectCandidates(all []Candidate, spec string) ([]Candidate, error) {
	spec = strings.TrimSpace(spec)
	var out []Candidate
	switch spec {
	case "":
		for _, c := range all {
			if !c.Optional {
				out = append(out, c)
			}
		}
		return out, nil
	case "all":
		return all, nil
	}
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		found := false
		for _, c := range all {
			if c.Name == name {
				out, found = append(out, c), true
			}
		}
		if !found {
			names := make([]string, len(all))
			for i, c := range all {
				names[i] = c.Name
			}
			return nil, fmt.Errorf("llmbench: unknown candidate %q (have %s)", name, strings.Join(names, ", "))
		}
	}
	return out, nil
}

// MissingKeys returns the credential variables the selected arms need that are
// not set, each once.
func MissingKeys(selected []Candidate) []string {
	var missing []string
	seen := map[string]bool{}
	for _, c := range selected {
		if c.KeyEnv == "" || seen[c.KeyEnv] {
			continue
		}
		seen[c.KeyEnv] = true
		if os.Getenv(c.KeyEnv) == "" {
			missing = append(missing, c.KeyEnv)
		}
	}
	return missing
}
