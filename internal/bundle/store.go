package bundle

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// This file is the bundle-owned store seam (#451): import and export depend on
// these consumer-defined slices of the persistence layer instead of the
// concrete *storage.Store, so the heart of ADR-0053 — fresh-ID minting,
// cross-reference remapping, duplicate detection, secrets exclusion — runs
// against an in-memory fake in unit tests, while the Postgres adapter keeps
// the genuinely SQL-shaped behavior (real rollback, constraints, the
// auto-Butler trigger) under the integration suite.

// ExportStore is the narrow read surface [Export] serializes a Campaign from;
// *storage.Store satisfies it structurally and tests fake it. It enumerates
// EXACTLY the allowlisted reads (ADR-0053 §2): campaign, agents, per-agent
// grants, KG nodes/edges, characters, and — only when history is requested —
// voice sessions, transcript lines, and transcript chunks. The
// secrets-exclusion property is visible ON this seam: there is no method that
// could read provider_config, deployment_config, users, or auth sessions, so
// no secret byte can reach a bundle through it. Never widen it with one.
//
// Read contracts Export relies on: bundle layout is list order, so a
// deterministic bundle needs every List method to return a stable order. The
// real store's orderings, which an adapter must reproduce, are: ListAgents
// (agent_role, name) — also the order the importer re-creates agents in,
// which assigns speaker-colour slots; ListToolGrants (tool_name); ListNodes
// (node_type, lower(name), id) — node_type is a Postgres ENUM ordered by
// declaration, npc before location; ListEdges and ListTranscriptChunks
// (created_at, id); ListCharacters (lower(name), id); ListVoiceSessions
// (started_at DESC, id DESC); ListTranscriptLines (seq — the ADR-0040 replay
// order). ListTranscriptChunks with includeVectors=false returns Embedding ""
// — Export always passes false (ADR-0053 §3, the destination re-embeds).
type ExportStore interface {
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	ListAgents(ctx context.Context, campaignID uuid.UUID) ([]storage.Agent, error)
	ListToolGrants(ctx context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error)
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
	ListEdges(ctx context.Context, campaignID uuid.UUID) ([]storage.KGEdge, error)
	ListCharacters(ctx context.Context, campaignID uuid.UUID) ([]storage.Character, error)
	ListVoiceSessions(ctx context.Context, campaignID uuid.UUID, limit int) ([]storage.VoiceSession, error)
	ListTranscriptLines(ctx context.Context, sessionID uuid.UUID) ([]storage.TranscriptLine, error)
	ListTranscriptChunks(ctx context.Context, campaignID uuid.UUID, includeVectors bool) ([]storage.ExportChunk, error)
}

// ImportStore is the tx-bound write surface one import runs against —
// the campaign-aggregate methods [Import]'s transaction body actually uses;
// *storage.Store satisfies it structurally and tests fake it. Callers only
// ever receive one through [TxRunner.InTx], so every write lands inside the
// single import transaction.
//
// Adapter contracts the importer leans on (an adapter that breaks one breaks
// the import, so the fake emulates them all):
//
//   - CreateCampaign creates the campaign's Butler as a side effect — the
//     ADR-0009 auto-Butler (name 'Glyphoxa', address_only true) with its
//     default grant set — so GetButler right after CreateCampaign returns a
//     row and a second Butler insert is impossible.
//   - CreateAgent refuses a second Butler in a Campaign (the partial unique
//     index behind ADR-0009).
//   - UpdateAgent force-keeps a Butler's address_only true (ADR-0024), never
//     changes agent_role, and yields storage.ErrNotFound for an (id,
//     campaign) miss.
//   - CreateToolGrant refuses a duplicate (agent, tool) — an Agent grants a
//     Tool at most once (ADR-0029); DeleteToolGrant yields storage.ErrNotFound
//     for a grant that was never there.
//   - ImportVoiceSession writes the given timestamps/status/line_count
//     VERBATIM (unlike the live CreateVoiceSession) and returns a minted id.
//   - UpsertTranscriptLine upserts on the (voice_session_id, line_id) replay
//     key (ADR-0040); on conflict seq is NOT updated — it is the replay
//     ordering key, fixed at first insert.
//   - InsertTranscriptChunk never writes an embedding or embedding_model
//     (ADR-0011): imported chunks are always the destination embedworker's
//     backlog.
type ImportStore interface {
	CreateCampaign(ctx context.Context, c storage.NewCampaign) (uuid.UUID, error)
	GetButler(ctx context.Context, campaignID uuid.UUID) (storage.Agent, error)
	CreateAgent(ctx context.Context, a storage.NewAgent) (uuid.UUID, error)
	UpdateAgent(ctx context.Context, a storage.AgentUpdate) (storage.Agent, error)
	ListToolGrants(ctx context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error)
	CreateToolGrant(ctx context.Context, g storage.NewToolGrant) (uuid.UUID, error)
	DeleteToolGrant(ctx context.Context, agentID uuid.UUID, toolName string) error
	CreateNode(ctx context.Context, n storage.NewKGNode) (storage.KGNode, error)
	SetNodeAgent(ctx context.Context, campaignID, nodeID uuid.UUID, agentID uuid.NullUUID) (storage.KGNode, error)
	CreateEdge(ctx context.Context, e storage.NewKGEdge) (storage.KGEdge, error)
	CreateCharacter(ctx context.Context, c storage.NewCharacter) (uuid.UUID, error)
	ImportVoiceSession(ctx context.Context, v storage.VoiceSession) (uuid.UUID, error)
	UpsertTranscriptLine(ctx context.Context, l storage.TranscriptLine) error
	InsertTranscriptChunk(ctx context.Context, c storage.TranscriptChunk) (uuid.UUID, error)
}

// TxRunner is [Import]'s transaction entry point: InTx runs fn against a
// tx-bound [ImportStore], committing when fn returns nil and rolling back when
// it doesn't. The bundle atomicity requirement (#291/#292 — one transaction
// for the whole ingest, domain grains and history alike; import stays a
// synchronous RPC per ADR-0049) lives HERE: fn runs as ONE unit, so every row
// an import writes lands together or not at all — a mid-bundle failure leaves
// no partial Campaign.
//
// The flatten expectation — previously an implicit property of
// *storage.Store.InTx this package silently relied on — is part of the seam
// contract: an ImportStore method that internally opens its own transaction
// (storage's CreateEdge does, via a nested InTx) must FLATTEN into the ambient
// import transaction. It joins the outer transaction and gains NO independent
// rollback boundary (#291): a later error still rolls back the whole import,
// including such an inner "committed" call. An adapter giving nested calls
// real savepoint semantics would not break the importer, but one giving them
// independent COMMITs would — that is the contract violation this note
// exists to forbid.
type TxRunner interface {
	InTx(ctx context.Context, fn func(tx ImportStore) error) error
}

// PGStore adapts *storage.Store to the bundle seam — the Postgres adapter the
// production handler and the seed path pass to [Import]. The read/write
// slices need no adapting (the embedded Store satisfies [ExportStore] and
// [ImportStore] structurally, per the assertions below); this wrapper exists
// only because the concrete InTx types its closure over *storage.Store, so
// [TxRunner] needs the tx-bound store re-typed as the [ImportStore] slice.
// Behavior is the embedded Store's exactly: one BEGIN/COMMIT with rollback on
// error, and nested InTx calls flattening into the ambient transaction (#291).
type PGStore struct{ *storage.Store }

// InTx implements [TxRunner] over the embedded Store's single-transaction
// runner.
func (p PGStore) InTx(ctx context.Context, fn func(tx ImportStore) error) error {
	return p.Store.InTx(ctx, func(tx *storage.Store) error { return fn(tx) })
}

// Compile-time proofs the Postgres adapter satisfies the whole seam: the
// concrete store IS the read and write slices, and PGStore adds the
// transaction entry point on top.
var (
	_ ExportStore = (*storage.Store)(nil)
	_ ImportStore = (*storage.Store)(nil)
	_ TxRunner    = PGStore{}
	_ ExportStore = PGStore{}
)
