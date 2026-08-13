package bundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// fakeStore is the in-memory adapter of the bundle store seam (#451): it
// implements bundle.ExportStore + bundle.ImportStore + bundle.TxRunner over
// plain slices, so the remap/codec/secrets logic of import and export runs
// under `go test` with no Postgres. It emulates every adapter contract the
// [bundle.ImportStore] doc enumerates — including the ADR-0009 auto-Butler
// trigger, which no other fake in the repo needed before — because the
// importer's Butler-merge path is built on them.
//
// What it deliberately does NOT emulate: transaction rollback. InTx runs the
// function directly against the same state (the flatten shape of a tx-bound
// store) and leaves any partial writes behind on error, so import atomicity
// remains proven ONLY by the Postgres integration tests
// (TestImportMidBundleFailureRollsBack, TestImportHistoryRollsBackWithPart1) —
// a fake-backed test asserts returned errors and logic, never all-or-nothing
// persistence. Also absent: the NPC-only node↔agent CHECK on SetNodeAgent's
// link path, which stays integration-proven.
//
// Slices keep insertion order as a DETERMINISTIC stand-in for the real
// (created_at, id) list orderings — a stand-in, not parity: Postgres now() is
// the transaction timestamp, so every row one import writes shares a
// created_at and the real tie-break is random-UUID order. Tests must not pin
// the relative order of same-import edges or chunks beyond what the bundle
// format itself guarantees.
type fakeStore struct {
	campaigns  []storage.Campaign
	agents     []storage.Agent
	grants     []storage.ToolGrant
	nodes      []storage.KGNode
	edges      []storage.KGEdge
	characters []storage.Character
	sessions   []storage.VoiceSession
	lines      []storage.TranscriptLine
	chunks     []storage.TranscriptChunk

	// The world #547 added. Aspects and tags hang off the node rows themselves
	// (kg_node projects Aspects), so only the standalone tables are held here.
	tags        map[uuid.UUID][]string
	maps        []storage.CampaignMap
	pins        map[uuid.UUID][]storage.MapPin
	boards      []storage.KGBoard
	appearances []storage.NodeAppearance
	images      map[string][]byte
	imageTypes  map[string]string

	// The Butler planning chat #592 added.
	threads      []storage.PlanningThread
	planningMsgs map[uuid.UUID][]storage.PlanningMessage
	// deletedImages records the rollback path's cleanup so a test can prove a
	// failed import does not strand bytes.
	deletedImages []string
}

// Compile-time proofs the fake satisfies the whole seam — the second adapter
// that, with PGStore, makes the seam real (#451).
var (
	_ bundle.ExportStore    = (*fakeStore)(nil)
	_ bundle.ImportStore    = (*fakeStore)(nil)
	_ bundle.TxRunner       = (*fakeStore)(nil)
	_ bundle.MapImageWriter = (*fakeStore)(nil)
)

func newFakeStore() *fakeStore {
	return &fakeStore{
		tags:         map[uuid.UUID][]string{},
		pins:         map[uuid.UUID][]storage.MapPin{},
		images:       map[string][]byte{},
		imageTypes:   map[string]string{},
		planningMsgs: map[uuid.UUID][]storage.PlanningMessage{},
	}
}

// ── #547: maps, pins, aspects, tags, boards, appearances ────────────────────
//
// These emulate the real adapter's contracts, not merely its signatures: the
// composite FKs are enforced by hand (a pin whose map or node is not in this
// campaign is refused), UpdateMap replaces the whole editor field set, and
// UpdateBoard is one call for the rename AND the entries.

func (f *fakeStore) CampaignTags(_ context.Context, campaignID uuid.UUID) ([]storage.TaggedNode, error) {
	var out []storage.TaggedNode
	for _, n := range f.nodes {
		if n.CampaignID != campaignID {
			continue
		}
		for _, t := range f.tags[n.ID] {
			out = append(out, storage.TaggedNode{NodeID: n.ID, Tag: t})
		}
	}
	return out, nil
}

func (f *fakeStore) SetNodeTags(_ context.Context, campaignID, nodeID uuid.UUID, tags []string) error {
	for _, n := range f.nodes {
		if n.ID == nodeID && n.CampaignID == campaignID {
			f.tags[nodeID] = append([]string(nil), tags...)
			return nil
		}
	}
	return storage.ErrNotFound
}

func (f *fakeStore) ReplaceNodeAspects(_ context.Context, campaignID, nodeID uuid.UUID, w storage.KGNodeAspectWrite) error {
	for i := range f.nodes {
		if f.nodes[i].ID != nodeID || f.nodes[i].CampaignID != campaignID {
			continue
		}
		out := make([]storage.KGNodeAspect, 0, len(w.Rows))
		for j, r := range w.Rows {
			out = append(out, storage.KGNodeAspect{
				ID: uuid.New(), Position: j, Key: r.Key, Value: r.Value, GMPrivate: r.GMPrivate,
			})
		}
		f.nodes[i].Aspects = out
		return nil
	}
	return storage.ErrNotFound
}

func (f *fakeStore) UpdateEdgeDetails(_ context.Context, campaignID, id uuid.UUID, note string, disposition int) (storage.KGEdge, error) {
	if disposition < -2 || disposition > 2 {
		return storage.KGEdge{}, storage.ErrInvalidDisposition
	}
	for i := range f.edges {
		if f.edges[i].ID == id && f.edges[i].CampaignID == campaignID {
			f.edges[i].Note = note
			f.edges[i].Disposition = disposition
			return f.edges[i], nil
		}
	}
	return storage.KGEdge{}, storage.ErrNotFound
}

func (f *fakeStore) ListMaps(_ context.Context, campaignID uuid.UUID) ([]storage.CampaignMap, error) {
	var out []storage.CampaignMap
	for _, m := range f.maps {
		if m.CampaignID == campaignID {
			out = append(out, m)
		}
	}
	// The real read orders by (name, id); a deterministic bundle depends on it.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (f *fakeStore) CreateMap(_ context.Context, m storage.NewCampaignMap) (storage.CampaignMap, error) {
	created := storage.CampaignMap{
		ID: uuid.New(), CampaignID: m.CampaignID, Name: m.Name, BlobKey: m.BlobKey,
		WidthPx: m.WidthPx, HeightPx: m.HeightPx,
		ParentMapID: m.ParentMapID, AnchorNodeID: m.AnchorNodeID, GMPrivate: m.GMPrivate,
	}
	// The composite FK: an anchor from another campaign is refused, not stored.
	if m.AnchorNodeID.Valid && !f.nodeInCampaign(m.AnchorNodeID.UUID, m.CampaignID) {
		return storage.CampaignMap{}, storage.ErrNotFound
	}
	f.maps = append(f.maps, created)
	return created, nil
}

func (f *fakeStore) UpdateMap(_ context.Context, u storage.CampaignMapUpdate) (storage.CampaignMap, error) {
	if u.ParentMapID.Valid && u.ParentMapID.UUID == u.ID {
		return storage.CampaignMap{}, storage.ErrInvalidMapParent
	}
	for i := range f.maps {
		if f.maps[i].ID != u.ID || f.maps[i].CampaignID != u.CampaignID {
			continue
		}
		if u.ParentMapID.Valid && !f.mapInCampaign(u.ParentMapID.UUID, u.CampaignID) {
			return storage.CampaignMap{}, storage.ErrNotFound
		}
		// Replaces the WHOLE editor field set, exactly as the real UPDATE does.
		f.maps[i].Name = u.Name
		f.maps[i].ParentMapID = u.ParentMapID
		f.maps[i].AnchorNodeID = u.AnchorNodeID
		f.maps[i].GMPrivate = u.GMPrivate
		return f.maps[i], nil
	}
	return storage.CampaignMap{}, storage.ErrNotFound
}

func (f *fakeStore) ListPins(_ context.Context, campaignID, mapID uuid.UUID) ([]storage.MapPin, error) {
	if !f.mapInCampaign(mapID, campaignID) {
		return nil, nil
	}
	return f.pins[mapID], nil
}

func (f *fakeStore) CreatePin(_ context.Context, n storage.NewMapPin) (storage.MapPin, error) {
	if n.X < 0 || n.X > 1 || n.Y < 0 || n.Y > 1 {
		return storage.MapPin{}, storage.ErrInvalidPin
	}
	// Both composite FKs.
	if !f.mapInCampaign(n.MapID, n.CampaignID) || !f.nodeInCampaign(n.NodeID, n.CampaignID) {
		return storage.MapPin{}, storage.ErrNotFound
	}
	for _, p := range f.pins[n.MapID] {
		if p.NodeID == n.NodeID {
			return storage.MapPin{}, storage.ErrConflict // one pin per node per map
		}
	}
	created := storage.MapPin{
		ID: uuid.New(), MapID: n.MapID, CampaignID: n.CampaignID, NodeID: n.NodeID,
		X: n.X, Y: n.Y, LabelOverride: n.LabelOverride, GMPrivate: n.GMPrivate,
	}
	f.pins[n.MapID] = append(f.pins[n.MapID], created)
	return created, nil
}

func (f *fakeStore) ListBoards(_ context.Context, campaignID uuid.UUID) ([]storage.KGBoard, error) {
	var out []storage.KGBoard
	for _, b := range f.boards {
		if b.CampaignID == campaignID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateBoard(_ context.Context, campaignID uuid.UUID, name string) (storage.KGBoard, error) {
	b := storage.KGBoard{ID: uuid.New(), CampaignID: campaignID, Name: name}
	f.boards = append(f.boards, b)
	return b, nil
}

func (f *fakeStore) UpdateBoard(_ context.Context, campaignID, id uuid.UUID, name string, nodeIDs []uuid.UUID) error {
	for i := range f.boards {
		if f.boards[i].ID != id || f.boards[i].CampaignID != campaignID {
			continue
		}
		for _, n := range nodeIDs {
			if !f.nodeInCampaign(n, campaignID) {
				return storage.ErrNotFound
			}
		}
		f.boards[i].Name = name
		f.boards[i].NodeIDs = append([]uuid.UUID(nil), nodeIDs...)
		return nil
	}
	return storage.ErrNotFound
}

func (f *fakeStore) ListSessionAppearances(_ context.Context, sessionID uuid.UUID) ([]storage.SessionAppearance, error) {
	var out []storage.SessionAppearance
	for _, a := range f.appearances {
		if a.VoiceSessionID == sessionID {
			out = append(out, storage.SessionAppearance{NodeID: a.NodeID, LineID: a.LineID, At: a.At})
		}
	}
	return out, nil
}

func (f *fakeStore) RecordNodeAppearances(_ context.Context, rows []storage.NodeAppearance) error {
	// ON CONFLICT DO NOTHING over (node, session, line) — AND the composite FK to
	// transcript_line, which Postgres enforces and a fake that skipped it would
	// hide: an appearance naming a line the import did not write fails the FK and
	// rolls the WHOLE bundle back.
	for _, r := range rows {
		if !f.lineExists(r.VoiceSessionID, r.LineID) {
			return fmt.Errorf("fake: node_appearance_line_fk: no line %q in session %s", r.LineID, r.VoiceSessionID)
		}
		if !f.nodeInCampaign(r.NodeID, r.CampaignID) {
			return fmt.Errorf("fake: node_appearance_node_fk: node %s not in campaign %s", r.NodeID, r.CampaignID)
		}
		dup := false
		for _, e := range f.appearances {
			if e.NodeID == r.NodeID && e.VoiceSessionID == r.VoiceSessionID && e.LineID == r.LineID {
				dup = true
				break
			}
		}
		if !dup {
			f.appearances = append(f.appearances, r)
		}
	}
	return nil
}

// ── #592: the Butler planning chat ──────────────────────────────────────────
//
// Same discipline as the #547 block above: the composite FK is enforced by
// hand (a message appended to a thread outside the campaign is ErrNotFound),
// the role CHECK from migration 00053 is refused like the real INSERT's
// constraint, and AppendPlanningMessage derives seq as max+1 exactly as the
// real writer does — the seam contract the importer's in-order replay rides on.

func (f *fakeStore) ListPlanningThreads(_ context.Context, campaignID uuid.UUID) ([]storage.PlanningThread, error) {
	var out []storage.PlanningThread
	for _, th := range f.threads {
		if th.CampaignID == campaignID {
			out = append(out, th)
		}
	}
	// Insertion order — a deterministic stand-in for the real (updated_at DESC,
	// id) read: every row one import writes shares the transaction's now(), so
	// the real tie-break is random-UUID order (see the type comment).
	return out, nil
}

func (f *fakeStore) CreatePlanningThread(_ context.Context, campaignID uuid.UUID, title string) (storage.PlanningThread, error) {
	th := storage.PlanningThread{ID: uuid.New(), CampaignID: campaignID, Title: title}
	f.threads = append(f.threads, th)
	return th, nil
}

func (f *fakeStore) ListPlanningMessages(_ context.Context, campaignID, threadID uuid.UUID) ([]storage.PlanningMessage, error) {
	// Like the real read: an unknown thread yields an empty slice, not an error.
	if !f.threadInCampaign(threadID, campaignID) {
		return nil, nil
	}
	out := append([]storage.PlanningMessage(nil), f.planningMsgs[threadID]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (f *fakeStore) AppendPlanningMessage(_ context.Context, campaignID, threadID uuid.UUID, role, content string) (storage.PlanningMessage, error) {
	// The composite FK: an unknown thread, or one outside the campaign, is
	// storage.ErrNotFound — never a silent insert.
	if !f.threadInCampaign(threadID, campaignID) {
		return storage.PlanningMessage{}, storage.ErrNotFound
	}
	// The role CHECK (migration 00053) — the importer validates first, so this
	// firing means a caller bypassed that gate.
	if role != storage.PlanningRoleUser && role != storage.PlanningRoleAssistant {
		return storage.PlanningMessage{}, fmt.Errorf("fake: planning_message_role_check: %q", role)
	}
	var maxSeq int64
	for _, m := range f.planningMsgs[threadID] {
		if m.Seq > maxSeq {
			maxSeq = m.Seq
		}
	}
	m := storage.PlanningMessage{
		ID: uuid.New(), ThreadID: threadID, CampaignID: campaignID,
		Seq: maxSeq + 1, Role: role, Content: content,
	}
	f.planningMsgs[threadID] = append(f.planningMsgs[threadID], m)
	return m, nil
}

func (f *fakeStore) threadInCampaign(id, campaignID uuid.UUID) bool {
	for _, th := range f.threads {
		if th.ID == id && th.CampaignID == campaignID {
			return true
		}
	}
	return false
}

func (f *fakeStore) lineExists(sessionID uuid.UUID, lineID string) bool {
	for _, l := range f.lines {
		if l.VoiceSessionID == sessionID && l.LineID == lineID {
			return true
		}
	}
	return false
}

func (f *fakeStore) ReadMapImage(_ context.Context, key string) ([]byte, string, error) {
	data, ok := f.images[key]
	if !ok {
		return nil, "", storage.ErrNotFound
	}
	return data, f.imageTypes[key], nil
}

func (f *fakeStore) WriteMapImage(_ context.Context, key, contentType string, data []byte) error {
	f.images[key] = append([]byte(nil), data...)
	f.imageTypes[key] = contentType
	return nil
}

func (f *fakeStore) DeleteMapImage(_ context.Context, key string) error {
	delete(f.images, key)
	delete(f.imageTypes, key)
	f.deletedImages = append(f.deletedImages, key)
	return nil
}

func (f *fakeStore) nodeInCampaign(id, campaignID uuid.UUID) bool {
	for _, n := range f.nodes {
		if n.ID == id && n.CampaignID == campaignID {
			return true
		}
	}
	return false
}

func (f *fakeStore) mapInCampaign(id, campaignID uuid.UUID) bool {
	for _, m := range f.maps {
		if m.ID == id && m.CampaignID == campaignID {
			return true
		}
	}
	return false
}

// commitFailTx runs the tx body against the fake and then fails the way a
// COMMIT can (serialization failure, dropped connection) — the only shape in
// which Import errors while DroppedParticipantRefs is already nonzero, since
// history is the last in-tx step.
type commitFailTx struct{ *fakeStore }

func (c commitFailTx) InTx(ctx context.Context, fn func(tx bundle.ImportStore) error) error {
	if err := fn(c.fakeStore); err != nil {
		return err
	}
	return errors.New("fake: commit failed")
}

// InTx implements [bundle.TxRunner] by flattening: fn runs directly against
// the fake's state, mirroring how a tx-bound *storage.Store runs a nested InTx
// in the ambient transaction. See the type comment for what that means for
// rollback (nothing — atomicity is the integration suite's job).
func (f *fakeStore) InTx(_ context.Context, fn func(tx bundle.ImportStore) error) error {
	return fn(f)
}

// butlerDefaultGrants is the auto-Butler trigger's default grant set as of
// migration 00027 (dice + the two knowledge tools + recap), tool names only —
// every default grant carries a NULL config.
var butlerDefaultGrants = []string{"dice", "transcript_search", "kg_query", "recap"}

// CreateCampaign mints the campaign row and emulates the ADR-0009 auto-Butler
// trigger: the campaign's 'Glyphoxa' Butler (address_only true, voice at the
// '{}' column default) plus its default grants land as a side effect, exactly
// like the Postgres trigger create_campaign_butler().
func (f *fakeStore) CreateCampaign(_ context.Context, c storage.NewCampaign) (uuid.UUID, error) {
	id := uuid.New()
	f.campaigns = append(f.campaigns, storage.Campaign{
		ID: id, TenantID: c.TenantID, Name: c.Name, System: c.System, Language: c.Language,
	})
	butlerID := uuid.New()
	f.agents = append(f.agents, storage.Agent{
		ID: butlerID, CampaignID: id, Role: storage.AgentRoleButler,
		Name: "Glyphoxa", Voice: json.RawMessage(`{}`), AddressOnly: true, Aliases: []string{},
	})
	for _, tool := range butlerDefaultGrants {
		f.grants = append(f.grants, storage.ToolGrant{ID: uuid.New(), AgentID: butlerID, ToolName: tool})
	}
	return id, nil
}

func (f *fakeStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	for _, c := range f.campaigns {
		if c.ID == id {
			return c, nil
		}
	}
	return storage.Campaign{}, storage.ErrNotFound
}

func (f *fakeStore) GetButler(_ context.Context, campaignID uuid.UUID) (storage.Agent, error) {
	for _, a := range f.agents {
		if a.CampaignID == campaignID && a.Role == storage.AgentRoleButler {
			return a, nil
		}
	}
	return storage.Agent{}, storage.ErrNotFound
}

// CreateAgent enforces the ADR-0009 partial unique index: a second Butler in a
// Campaign is refused. A Character gets the next roster-index speaker slot
// (the real store also wraps at the palette size — irrelevant here).
func (f *fakeStore) CreateAgent(_ context.Context, a storage.NewAgent) (uuid.UUID, error) {
	if a.Role == storage.AgentRoleButler {
		for _, existing := range f.agents {
			if existing.CampaignID == a.CampaignID && existing.Role == storage.AgentRoleButler {
				return uuid.Nil, fmt.Errorf("fake: second butler in campaign %s violates unique index", a.CampaignID)
			}
		}
	}
	slot := 0
	if a.Role == storage.AgentRoleCharacter {
		for _, existing := range f.agents {
			if existing.CampaignID == a.CampaignID && existing.Role == storage.AgentRoleCharacter {
				slot++
			}
		}
	}
	id := uuid.New()
	f.agents = append(f.agents, storage.Agent{
		ID: id, CampaignID: a.CampaignID, Role: a.Role,
		Name: a.Name, Title: a.Title, Persona: a.Persona,
		Voice: defaultVoice(a.Voice), VoiceProviderConfigID: a.VoiceProviderConfigID,
		LLMProviderConfigID: a.LLMProviderConfigID, AddressOnly: a.AddressOnly,
		SpeakerColor: slot, Aliases: defaultAliases(a.Aliases),
	})
	return id, nil
}

// UpdateAgent scopes the write to (id, campaign_id) → storage.ErrNotFound on a
// miss, never changes agent_role, and force-keeps a Butler's address_only true
// (ADR-0024) — the seam contracts the Butler merge rides on.
func (f *fakeStore) UpdateAgent(_ context.Context, a storage.AgentUpdate) (storage.Agent, error) {
	for i := range f.agents {
		ag := &f.agents[i]
		if ag.ID != a.ID || ag.CampaignID != a.CampaignID {
			continue
		}
		ag.Name, ag.Title, ag.Persona = a.Name, a.Title, a.Persona
		ag.Voice = defaultVoice(a.Voice)
		ag.VoiceProviderConfigID = a.VoiceProviderConfigID
		ag.LLMProviderConfigID = a.LLMProviderConfigID
		ag.AddressOnly = a.AddressOnly || ag.Role == storage.AgentRoleButler
		ag.Aliases = defaultAliases(a.Aliases)
		return *ag, nil
	}
	return storage.Agent{}, storage.ErrNotFound
}

func (f *fakeStore) ListAgents(_ context.Context, campaignID uuid.UUID) ([]storage.Agent, error) {
	var out []storage.Agent
	for _, a := range f.agents {
		if a.CampaignID == campaignID {
			out = append(out, a)
		}
	}
	// ORDER BY agent_role, name — 'butler' sorts before 'character'.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (f *fakeStore) ListToolGrants(_ context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error) {
	var out []storage.ToolGrant
	for _, g := range f.grants {
		if g.AgentID == agentID {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ToolName < out[j].ToolName })
	return out, nil
}

// CreateToolGrant refuses a duplicate (agent, tool) like the UNIQUE index
// (ADR-0029); an empty Config normalizes to nil, the shape a SQL NULL scans
// back as.
func (f *fakeStore) CreateToolGrant(_ context.Context, g storage.NewToolGrant) (uuid.UUID, error) {
	for _, existing := range f.grants {
		if existing.AgentID == g.AgentID && existing.ToolName == g.ToolName {
			return uuid.Nil, fmt.Errorf("fake: duplicate tool grant (%s/%s) violates unique index", g.AgentID, g.ToolName)
		}
	}
	id := uuid.New()
	config := g.Config
	if len(config) == 0 {
		config = nil
	}
	f.grants = append(f.grants, storage.ToolGrant{ID: id, AgentID: g.AgentID, ToolName: g.ToolName, Config: config})
	return id, nil
}

func (f *fakeStore) DeleteToolGrant(_ context.Context, agentID uuid.UUID, toolName string) error {
	for i, g := range f.grants {
		if g.AgentID == agentID && g.ToolName == toolName {
			f.grants = append(f.grants[:i], f.grants[i+1:]...)
			return nil
		}
	}
	return storage.ErrNotFound
}

// nodeTypeRank orders node types the way Postgres orders the kg_node_type
// ENUM — by declaration, not lexicographically (npc sorts before location).
var nodeTypeRank = func() map[storage.KGNodeType]int {
	m := make(map[storage.KGNodeType]int)
	for i, tp := range kgvocab.NodeTypes() {
		m[storage.KGNodeType(tp)] = i
	}
	return m
}()

// CreateNode refuses a node type outside the kg_node_type ENUM, like the
// real INSERT's ::kg_node_type cast.
func (f *fakeStore) CreateNode(_ context.Context, n storage.NewKGNode) (storage.KGNode, error) {
	if _, ok := nodeTypeRank[n.Type]; !ok {
		return storage.KGNode{}, fmt.Errorf("fake: invalid input value for enum kg_node_type: %q", n.Type)
	}
	node := storage.KGNode{
		ID: uuid.New(), CampaignID: n.CampaignID, Type: n.Type,
		Name: n.Name, Body: n.Body, GMPrivate: n.GMPrivate,
	}
	f.nodes = append(f.nodes, node)
	return node, nil
}

// SetNodeAgent scopes to (campaign, node) → storage.ErrNotFound on a miss and
// refuses linking an agent already linked to another node, like the
// kg_node_agent_unique index (one node per agent) → storage.ErrConflict. The
// real store's NPC-only CHECK on the link stays integration-proven; the fake
// links any node type.
func (f *fakeStore) SetNodeAgent(_ context.Context, campaignID, nodeID uuid.UUID, agentID uuid.NullUUID) (storage.KGNode, error) {
	if agentID.Valid {
		for _, n := range f.nodes {
			if n.AgentID.Valid && n.AgentID.UUID == agentID.UUID && n.ID != nodeID {
				return storage.KGNode{}, storage.ErrConflict
			}
		}
	}
	for i := range f.nodes {
		if f.nodes[i].ID == nodeID && f.nodes[i].CampaignID == campaignID {
			f.nodes[i].AgentID = agentID
			return f.nodes[i], nil
		}
	}
	return storage.KGNode{}, storage.ErrNotFound
}

func (f *fakeStore) ListNodes(_ context.Context, campaignID uuid.UUID) ([]storage.KGNode, error) {
	var out []storage.KGNode
	for _, n := range f.nodes {
		if n.CampaignID == campaignID {
			out = append(out, n)
		}
	}
	// ORDER BY node_type, lower(name), id — node_type is a Postgres ENUM,
	// ordered by declaration (npc before location), NOT lexicographically;
	// insertion order stands in for the id tie-break.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return nodeTypeRank[out[i].Type] < nodeTypeRank[out[j].Type]
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// CreateEdge mirrors the real createEdgeTx's pure-Go gates — none of which
// the import path satisfies by construction, since bundle edges travel
// verbatim: a self-edge is storage.ErrInvalidEdge, a missing/cross-campaign
// endpoint is storage.ErrNotFound, the ADR-0008 validity matrix and unknown
// edge types are refused via the exported storage.ValidateEdge, and a
// duplicate (from, to, type) is storage.ErrConflict like the UNIQUE index.
func (f *fakeStore) CreateEdge(_ context.Context, e storage.NewKGEdge) (storage.KGEdge, error) {
	if e.FromNodeID == e.ToNodeID {
		return storage.KGEdge{}, storage.ErrInvalidEdge
	}
	var fromType, toType storage.KGNodeType
	var okFrom, okTo bool
	for _, n := range f.nodes {
		if n.CampaignID != e.CampaignID {
			continue
		}
		if n.ID == e.FromNodeID {
			fromType, okFrom = n.Type, true
		}
		if n.ID == e.ToNodeID {
			toType, okTo = n.Type, true
		}
	}
	if !okFrom || !okTo {
		return storage.KGEdge{}, storage.ErrNotFound
	}
	if err := storage.ValidateEdge(e.Type, fromType, toType); err != nil {
		return storage.KGEdge{}, err
	}
	for _, existing := range f.edges {
		if existing.FromNodeID == e.FromNodeID && existing.ToNodeID == e.ToNodeID && existing.Type == e.Type {
			return storage.KGEdge{}, storage.ErrConflict
		}
	}
	edge := storage.KGEdge{
		ID: uuid.New(), CampaignID: e.CampaignID,
		FromNodeID: e.FromNodeID, ToNodeID: e.ToNodeID, Type: e.Type,
	}
	f.edges = append(f.edges, edge)
	return edge, nil
}

func (f *fakeStore) ListEdges(_ context.Context, campaignID uuid.UUID) ([]storage.KGEdge, error) {
	var out []storage.KGEdge
	for _, e := range f.edges {
		if e.CampaignID == campaignID {
			out = append(out, e)
		}
	}
	// Insertion order — a deterministic stand-in for the real (created_at,
	// id) read; same-import rows tie on created_at and order randomly by
	// UUID on Postgres (see the type comment).
	return out, nil
}

// CreateCharacter refuses a second Character for the same (campaign,
// discord_user_id) like the character_campaign_discord_user_idx UNIQUE index
// (one Character per Discord User per Campaign) → storage.ErrConflict.
func (f *fakeStore) CreateCharacter(_ context.Context, c storage.NewCharacter) (uuid.UUID, error) {
	for _, existing := range f.characters {
		if existing.CampaignID == c.CampaignID && existing.DiscordUserID == c.DiscordUserID {
			return uuid.Nil, storage.ErrConflict
		}
	}
	id := uuid.New()
	f.characters = append(f.characters, storage.Character{
		ID: id, CampaignID: c.CampaignID, Name: c.Name,
		Aliases: defaultAliases(c.Aliases), DiscordUserID: c.DiscordUserID,
	})
	return id, nil
}

func (f *fakeStore) ListCharacters(_ context.Context, campaignID uuid.UUID) ([]storage.Character, error) {
	var out []storage.Character
	for _, c := range f.characters {
		if c.CampaignID == campaignID {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ImportVoiceSession stores the given row VERBATIM (timestamps, status,
// line_count, end_reason) under a minted id — the contract that distinguishes
// it from the live CreateVoiceSession.
func (f *fakeStore) ImportVoiceSession(_ context.Context, v storage.VoiceSession) (uuid.UUID, error) {
	v.ID = uuid.New()
	f.sessions = append(f.sessions, v)
	return v.ID, nil
}

func (f *fakeStore) ListVoiceSessions(_ context.Context, campaignID uuid.UUID, limit int) ([]storage.VoiceSession, error) {
	var out []storage.VoiceSession
	// Reverse-insertion walk as a deterministic stand-in for the id DESC
	// tie-break (random UUIDs carry no recency on Postgres — see the type
	// comment); the load-bearing order is started_at DESC.
	for i := len(f.sessions) - 1; i >= 0; i-- {
		if f.sessions[i].CampaignID == campaignID {
			out = append(out, f.sessions[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UpsertTranscriptLine upserts on the (voice_session_id, line_id) replay key
// (ADR-0040): a conflict updates who/tag/kind/ts/text/speaker but NEVER seq —
// the replay ordering key is fixed at first insert.
func (f *fakeStore) UpsertTranscriptLine(_ context.Context, l storage.TranscriptLine) error {
	for i := range f.lines {
		if f.lines[i].VoiceSessionID == l.VoiceSessionID && f.lines[i].LineID == l.LineID {
			seq := f.lines[i].Seq
			f.lines[i] = l
			f.lines[i].Seq = seq
			return nil
		}
	}
	f.lines = append(f.lines, l)
	return nil
}

func (f *fakeStore) ListTranscriptLines(_ context.Context, sessionID uuid.UUID) ([]storage.TranscriptLine, error) {
	var out []storage.TranscriptLine
	for _, l := range f.lines {
		if l.VoiceSessionID == sessionID {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// InsertTranscriptChunk never records an embedding or embedding_model
// (ADR-0011) — like the real writer, which leaves the vector NULL for the
// destination embedworker. Arrays default to empty, never nil.
func (f *fakeStore) InsertTranscriptChunk(_ context.Context, c storage.TranscriptChunk) (uuid.UUID, error) {
	c.ID = uuid.New()
	c.EmbeddingModel = ""
	if c.SpeakerDiscordUserIDs == nil {
		c.SpeakerDiscordUserIDs = []string{}
	}
	if c.ParticipatedAgentIDs == nil {
		c.ParticipatedAgentIDs = []uuid.UUID{}
	}
	f.chunks = append(f.chunks, c)
	return c.ID, nil
}

// ListTranscriptChunks returns the campaign's chunks in insertion order — the
// deterministic stand-in for the real (created_at, id) read; same-import rows
// tie on created_at and order randomly by UUID on Postgres (see the type
// comment). Embedding is always "": the fake never holds a vector, so
// includeVectors has nothing to include — Export only ever passes false
// anyway (ADR-0053 §3).
func (f *fakeStore) ListTranscriptChunks(_ context.Context, campaignID uuid.UUID, _ bool) ([]storage.ExportChunk, error) {
	var out []storage.ExportChunk
	for _, c := range f.chunks {
		if c.CampaignID == campaignID {
			out = append(out, storage.ExportChunk{TranscriptChunk: c})
		}
	}
	return out, nil
}

// defaultVoice mirrors the store's write-side default: an empty voice persists
// as the '{}' column default.
func defaultVoice(v []byte) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage(`{}`)
	}
	return v
}

// defaultAliases mirrors the store's write-side default: nil persists as '{}'
// (empty array), never SQL NULL.
func defaultAliases(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}
