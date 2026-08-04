package mapgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Suggested pins (#541).
//
// "Suggest, never place." The model is asked ONE question — which of these
// entries the GM already has belong on this map — and it answers with a subset.
// It cannot name a place that does not exist, and it is never asked for a
// coordinate, because coordinates from a language model are confident and wrong,
// and the spatial layer is read by the Party Marker, both spatial Tools and every
// later feature. Garbage there is not a cosmetic problem.
//
// The GM then drags each suggestion into position, which is the existing
// unplaced-tray → click-the-map → CreatePin flow. Nothing here persists.

// TextCaller runs one metered text completion through the campaign's own LLM
// provider. *assist.Engine satisfies it via CallText — the provider resolution
// chain and the usage attribution live there and are not duplicated here.
type TextCaller interface {
	CallText(ctx context.Context, campaign storage.Campaign, feature, system, user string, maxTokens int) (string, error)
}

// maxSuggestCandidates bounds how many entries are offered to the model at once.
// A campaign with 800 entries would otherwise put its whole wiki in a prompt.
const maxSuggestCandidates = 120

// suggestMaxTokens bounds the reply. The answer is a list of numbers.
const suggestMaxTokens = 512

// suggestSystemPrompt is the fixed directive. It asks for INDICES, not ids: a
// model copying 36-character uuids is a model with 36 chances per entry to
// hallucinate a plausible-looking one, and an index is checkable against a slice.
const suggestSystemPrompt = "You are helping a tabletop-RPG game master decide which of their existing wiki entries " +
	"belong on a map of one location. You will be given the location's description and a numbered list of entries. " +
	"Reply with ONLY a JSON array of the numbers of the entries that are physically located at or inside that place — " +
	"for example [1,4,7]. Include an entry only if the description gives a real reason to think it is there. " +
	"An empty array is a correct and useful answer. No prose, no explanation, no code fences."

// WithTextCaller attaches the text seam the pin suggester needs. Without it,
// SuggestPins reports that suggestions are unavailable rather than guessing.
func (e *Engine) WithTextCaller(t TextCaller) *Engine {
	e.text = t
	return e
}

// SuggestPins returns the subset of `candidates` the model believes belong on the
// Map anchored at anchorNodeID. It writes nothing.
func (e *Engine) SuggestPins(ctx context.Context, campaign storage.Campaign, anchorNodeID uuid.UUID, candidates []storage.KGNode) ([]uuid.UUID, error) {
	if e.text == nil {
		return nil, fmt.Errorf("mapgen: pin suggestions are unavailable in this mode")
	}
	if e.seeds == nil {
		return nil, fmt.Errorf("mapgen: pin suggestions are unavailable in this mode")
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > maxSuggestCandidates {
		candidates = candidates[:maxSuggestCandidates]
	}

	// The SAME public-only read the image prompt uses. A private detail steering
	// which public entries get suggested is a quieter leak than a private name in
	// a picture, but it is the same leak, and one read means one rule.
	anchor, residents, err := e.seeds.MapSeedContext(ctx, campaign.ID, anchorNodeID)
	if err != nil {
		return nil, err
	}

	var user strings.Builder
	user.WriteString("The map depicts \"" + anchor.Name + "\".")
	if body := strings.TrimSpace(anchor.Body); body != "" {
		user.WriteString("\nDescription: " + truncateRunes(body, maxSeedBodyRunes))
	}
	if len(residents) > 0 {
		named := residents
		if len(named) > maxResidents {
			named = named[:maxResidents]
		}
		user.WriteString("\nAlready known to be there: " + strings.Join(named, ", ") + ".")
	}
	user.WriteString("\n\nEntries:")
	for i, n := range candidates {
		fmt.Fprintf(&user, "\n%d. %s (%s)", i+1, n.Name, n.Type)
	}

	raw, err := e.text.CallText(ctx, campaign, "map_pins", suggestSystemPrompt, user.String(), suggestMaxTokens)
	if err != nil {
		return nil, err
	}

	picked, perr := parseIndices(raw, len(candidates))
	if perr != nil {
		// An unusable reply is NOT an error the GM sees as a failure: the honest
		// outcome of "which of these belong here" being unanswerable is no
		// suggestions, and nothing was going to be written either way.
		e.log.Warn("mapgen: unusable pin suggestion reply",
			"campaign_id", campaign.ID, "err", perr, "raw_len", len(raw))
		return nil, nil
	}

	out := make([]uuid.UUID, 0, len(picked))
	for _, i := range picked {
		out = append(out, candidates[i].ID)
	}
	return out, nil
}

// parseIndices reads the model's JSON array of 1-based indices and returns them
// 0-based, deduped, dropping anything out of range. Out-of-range is dropped
// rather than erroring: one bad number should not discard eight good ones.
func parseIndices(raw string, n int) ([]int, error) {
	start := strings.IndexByte(raw, '[')
	end := strings.LastIndexByte(raw, ']')
	if start < 0 || end < start {
		return nil, fmt.Errorf("no JSON array in the reply")
	}
	var nums []int
	if err := json.Unmarshal([]byte(raw[start:end+1]), &nums); err != nil {
		return nil, fmt.Errorf("reply is not an array of numbers: %w", err)
	}
	seen := make(map[int]bool, len(nums))
	out := make([]int, 0, len(nums))
	for _, v := range nums {
		i := v - 1
		if i < 0 || i >= n || seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	return out, nil
}
