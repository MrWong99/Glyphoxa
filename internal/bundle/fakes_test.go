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
}

// Compile-time proofs the fake satisfies the whole seam — the second adapter
// that, with PGStore, makes the seam real (#451).
var (
	_ bundle.ExportStore = (*fakeStore)(nil)
	_ bundle.ImportStore = (*fakeStore)(nil)
	_ bundle.TxRunner    = (*fakeStore)(nil)
)

func newFakeStore() *fakeStore { return &fakeStore{} }

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
