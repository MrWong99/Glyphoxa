package assist

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// wireDraft is the JSON shape the knowledge prompt contracts the model to.
type wireDraft struct {
	Nodes []wireNode `json:"nodes"`
	Edges []wireEdge `json:"edges"`
}

type wireNode struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	GMPrivate bool   `json:"gm_private"`
}

type wireEdge struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Type string `json:"type"`
}

// parseDraft turns raw model text into a validated Draft. It is deliberately
// lenient about packaging (code fences, prose around the JSON object) and
// strict about content: a node with an unknown type or an empty name is
// DROPPED (with every incident edge, indices remapped), an edge that is
// out-of-vocabulary, out-of-range, self-referential, duplicate, or invalid
// under the ADR-0008 object-side matrix is DROPPED — so an applied draft can
// only fail on genuinely new conditions (e.g. a duplicate against a
// concurrently created edge). A draft with no valid nodes left errors.
func parseDraft(raw string) (Draft, error) {
	jsonText, ok := extractJSONObject(raw)
	if !ok {
		return Draft{}, errors.New("no JSON object in model response")
	}
	var w wireDraft
	if err := json.Unmarshal([]byte(jsonText), &w); err != nil {
		return Draft{}, fmt.Errorf("unmarshal model response: %w", err)
	}

	// Filter nodes; remap[i] is node i's post-filter index, -1 when dropped.
	// Pre-fill with -1: the cap `break` below leaves tail entries untouched, and
	// the zero value would silently alias them onto node 0.
	remap := make([]int, len(w.Nodes))
	for i := range remap {
		remap[i] = -1
	}
	nodes := make([]DraftNode, 0, len(w.Nodes))
	for i, n := range w.Nodes {
		name := strings.TrimSpace(n.Name)
		typ := strings.ToLower(strings.TrimSpace(n.Type))
		if name == "" || !kgvocab.ValidNodeType(typ) {
			continue
		}
		if len(nodes) == maxDraftNodes {
			break
		}
		remap[i] = len(nodes)
		nodes = append(nodes, DraftNode{
			Type:      typ,
			Name:      name,
			Body:      strings.TrimSpace(n.Body),
			GMPrivate: n.GMPrivate,
		})
	}
	if len(nodes) == 0 {
		return Draft{}, errors.New("no valid nodes in model response")
	}

	type edgeKey struct {
		from, to int
		typ      string
	}
	seen := make(map[edgeKey]struct{}, len(w.Edges))
	edges := make([]DraftEdge, 0, len(w.Edges))
	for _, e := range w.Edges {
		if len(edges) == maxDraftEdges {
			break
		}
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		if !kgvocab.ValidRelation(typ) {
			continue
		}
		if e.From < 0 || e.From >= len(remap) || e.To < 0 || e.To >= len(remap) {
			continue
		}
		from, to := remap[e.From], remap[e.To]
		if from < 0 || to < 0 || from == to {
			continue
		}
		// Same matrix the apply path enforces (storage.ValidateEdge): drop a
		// matrix-invalid edge here so the preview only shows edges that can land.
		if storage.ValidateEdge(
			storage.KGEdgeType(typ),
			storage.KGNodeType(nodes[from].Type),
			storage.KGNodeType(nodes[to].Type),
		) != nil {
			continue
		}
		k := edgeKey{from, to, typ}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		edges = append(edges, DraftEdge{FromIndex: from, ToIndex: to, Type: typ})
	}

	return Draft{Nodes: nodes, Edges: edges}, nil
}

// extractJSONObject returns the outermost {...} object in raw, tolerating code
// fences and surrounding prose. It scans brace depth outside JSON strings from
// the first '{'; when no balanced object closes, it reports ok=false.
func extractJSONObject(raw string) (string, bool) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return raw[start : i+1], true
			}
		}
	}
	return "", false
}

// stripFences removes a wrapping markdown code fence (``` or ```lang) from a
// persona draft when the model ignored the no-fences instruction.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```")
	if nl := strings.IndexByte(t, '\n'); nl >= 0 {
		// Drop a language tag on the opening fence line, if any.
		if firstLine := strings.TrimSpace(t[:nl]); firstLine == "" || !strings.ContainsAny(firstLine, " \t") {
			t = t[nl+1:]
		}
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	return strings.TrimSpace(t)
}
