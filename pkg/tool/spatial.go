package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Spatial queries as read-only Tools (#539, ADR-0060).
//
// The tempting move is to stuff "nearby places" into every NPC prompt. Don't:
// the facts block is capped and `renderFacts` stops at the first fact that would
// overrun, so anything added there SILENTLY EVICTS world knowledge. And the
// kgfacts read is deliberately one indexed OLTP query per turn with no cache,
// which is what makes a gm_private flip take effect on the very next turn —
// proximity queries do not belong on that path.
//
// A read-only Tool runs inline (ADR-0030's turn-commit deferral applies only to
// side-effecting Tools), costs nothing when unused, and is asked for only when the
// fiction calls for it.

// MaxNearbyResults caps what one whats_nearby call returns. A list longer than
// this is not an answer an NPC can say out loud.
const MaxNearbyResults = 8

// DefaultNearbyRadius is the fraction of the map's span treated as "nearby" when
// the model names no radius. Normalized units, so it means the same on a
// continent and on a tavern floor plan — which is the honest reading of "near"
// on a map whose scale the system does not know.
const DefaultNearbyRadius = 0.15

// Place is one spatial answer: a pinned entry, where it is, and how far. Storage-
// free, like every other pkg/tool payload.
type Place struct {
	// Name is the entry's label on that map.
	Name string
	// Kind is its GM-facing type label ("Location", "NPC", …).
	Kind string
	// MapName is the map it sits on.
	MapName string
	// Distance is normalized map units from the query origin; 0 for the origin.
	Distance float64
}

// SpatialReader is the read seam the spatial Tools need. It is a READ ONLY seam
// with no write anywhere on it, which is what makes these Tools structurally
// incapable of changing the world.
//
// Both methods are campaign-scoped from the turn's session, never from LLM
// arguments, and BOTH filter gm_private Maps, Pins and Nodes in the handler-side
// read — on the same seam-not-call-site principle kgfacts.PromptKG follows.
type SpatialReader interface {
	// Locate answers "where is this?" for a named entry: every Map it is pinned on.
	// An unknown name yields no places and no error — not knowing where something
	// is, is a legitimate answer.
	Locate(ctx context.Context, agentID, name string) ([]Place, error)
	// Nearby answers "what is around us?": Pins within radius of the Party Marker,
	// or of the calling Agent's own pinned Node when no marker is set.
	Nearby(ctx context.Context, agentID string, radius float64, limit int) ([]Place, error)
}

var locateEntityInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "The name of the person, place or thing to locate."
    }
  },
  "required": ["name"]
}`)

// LocateEntity answers "where is X?" from the Map layer (#539).
type LocateEntity struct{ src SpatialReader }

// NewLocateEntity builds the Tool over the spatial read seam. A nil src registers
// the Tool (the grant editor's catalog is identical in every mode) but its Execute
// reports it is unavailable rather than panicking.
func NewLocateEntity(src SpatialReader) *LocateEntity { return &LocateEntity{src: src} }

// Name implements [Tool].
func (*LocateEntity) Name() string { return "locate_entity" }

// Description implements [Tool].
func (*LocateEntity) Description() string {
	return "Find where someone or something is on the world's maps. " +
		"Use it when the conversation turns to where a person, place or object can be found."
}

// InputSchema implements [Tool].
func (*LocateEntity) InputSchema() json.RawMessage { return locateEntityInputSchema }

// ReadOnly implements [Tool]: this Tool only reads, so it runs inline within the
// turn (ADR-0030 defers only side-effecting Tools to turn commit).
func (*LocateEntity) ReadOnly() bool { return true }

// SupportsScope implements [Tool]: the reachable Maps are narrowed per Agent via
// the ADR-0029 grant scope — an innkeeper knowing the town's layout is correct;
// the same innkeeper knowing the enemy capital's is not.
func (*LocateEntity) SupportsScope() bool { return true }

// Execute implements [Tool].
func (t *LocateEntity) Execute(ctx context.Context, args json.RawMessage, grantConfig any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.src == nil {
		return "", fmt.Errorf("locate_entity: map lookups are unavailable in this mode")
	}
	// The scope is resolved for its side effect of REJECTING a misconfigured grant.
	// Its narrowing is applied handler-side by the reader, never by the model.
	if _, err := parseScope(grantConfig); err != nil {
		return "", fmt.Errorf("locate_entity: %w", err)
	}

	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("locate_entity: invalid arguments: %w", err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", fmt.Errorf("locate_entity: name is required")
	}

	places, err := t.src.Locate(ctx, CallerID(ctx), name)
	if err != nil {
		return "", fmt.Errorf("locate_entity: %w", err)
	}
	if len(places) == 0 {
		// Not knowing is a real answer, and a better one than an invented location.
		return fmt.Sprintf("You do not know where %s is.", name), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s is on:", name)
	for _, p := range places {
		fmt.Fprintf(&b, "\n- %s (%s)", p.MapName, p.Kind)
	}
	return b.String(), nil
}

var whatsNearbyInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "radius": {
      "type": "number",
      "description": "How far to look, as a fraction of the map (0.05 is very close, 0.5 is most of the map). Optional."
    }
  }
}`)

// WhatsNearby answers "what is around us?" from the Party Marker, or from the
// calling Agent's own pinned Node when no marker is set (#539).
type WhatsNearby struct{ src SpatialReader }

// NewWhatsNearby builds the Tool over the spatial read seam.
func NewWhatsNearby(src SpatialReader) *WhatsNearby { return &WhatsNearby{src: src} }

// Name implements [Tool].
func (*WhatsNearby) Name() string { return "whats_nearby" }

// Description implements [Tool].
func (*WhatsNearby) Description() string {
	return "Look around: the places, people and things near where the party currently is. " +
		"Use it when asked what is around, what can be seen, or what is close by."
}

// InputSchema implements [Tool].
func (*WhatsNearby) InputSchema() json.RawMessage { return whatsNearbyInputSchema }

// ReadOnly implements [Tool].
func (*WhatsNearby) ReadOnly() bool { return true }

// SupportsScope implements [Tool].
func (*WhatsNearby) SupportsScope() bool { return true }

// Execute implements [Tool].
func (t *WhatsNearby) Execute(ctx context.Context, args json.RawMessage, grantConfig any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.src == nil {
		return "", fmt.Errorf("whats_nearby: map lookups are unavailable in this mode")
	}
	if _, err := parseScope(grantConfig); err != nil {
		return "", fmt.Errorf("whats_nearby: %w", err)
	}

	var a struct {
		Radius float64 `json:"radius"`
	}
	// Absent/garbage arguments fall back to the default radius rather than failing:
	// "look around" is a question with an obvious default.
	_ = json.Unmarshal(args, &a)
	radius := a.Radius
	if radius <= 0 || radius > 1 {
		radius = DefaultNearbyRadius
	}

	places, err := t.src.Nearby(ctx, CallerID(ctx), radius, MaxNearbyResults)
	if err != nil {
		return "", fmt.Errorf("whats_nearby: %w", err)
	}
	if len(places) == 0 {
		return "There is nothing else nearby that you know of.", nil
	}

	var b strings.Builder
	b.WriteString("Nearby:")
	for _, p := range places {
		fmt.Fprintf(&b, "\n- %s (%s)", p.Name, p.Kind)
	}
	return b.String(), nil
}
