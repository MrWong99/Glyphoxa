//go:build integration

// Drives the #300 remember_knowledge write path against a real Postgres
// (testcontainers, ADR-0033): migration 00026 applies, the Tool handler resolves
// the caller's own linked Node and records a pending Knowledge Proposal with the
// exact jsonb shape, and deleting the authoring Agent cascades the proposal away.
// Nothing here touches kg_node/kg_edge — a proposal is a suggestion only (ADR-0052).

package knowledge_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// seedProposalWorld builds a Campaign with one Character NPC Agent linked to its
// own NPC Node, returning the store, pool, campaign, agent, and linked-node ids.
func seedProposalWorld(t *testing.T, dsn string) (*storage.Store, *pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID, campaignID, agentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ('Acme') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Lost Mine', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaignID); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (campaign_id, agent_role, name) VALUES ($1, 'character', 'Bartholomew')
		 RETURNING id`, campaignID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	store := storage.New(pool)
	node, err := store.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "Bartholomew",
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := store.SetNodeAgent(ctx, campaignID, node.ID,
		uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
		t.Fatalf("link node to agent: %v", err)
	}
	return store, pool, campaignID, agentID, node.ID
}

func TestRememberKnowledge_OwnNodeProposal_RealDB(t *testing.T) {
	dsn := startPostgres(t)
	store, pool, campaignID, agentID, nodeID := seedProposalWorld(t, dsn)
	// The adapter resolves its Campaign from the run context's session.Identity (#488).
	ctx := session.NewContext(context.Background(), session.Identity{CampaignID: campaignID})

	adapter := knowledge.New(store, store.PromptKG())
	rk := tool.NewRememberKnowledge(adapter)

	// The full handler path: own_node grant, caller stamped on the ctx.
	callCtx := tool.WithCaller(ctx, agentID.String())
	out, err := rk.Execute(callCtx,
		json.RawMessage(`{"kind":"fact","subject":"ignored by handler","fact":"I brew the finest ale in the realm"}`),
		json.RawMessage(`{"scope":"own_node"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Error("empty success text")
	}

	pending, err := store.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListPendingKnowledgeProposals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending proposals = %d, want 1", len(pending))
	}
	p := pending[0]
	if p.CampaignID != campaignID || p.AuthoringAgentID != agentID {
		t.Errorf("proposal scope = campaign %v / agent %v, want %v / %v",
			p.CampaignID, p.AuthoringAgentID, campaignID, agentID)
	}
	if p.Status != "pending" || p.ReviewedAt != nil {
		t.Errorf("proposal status = %q reviewedAt=%v, want pending/nil", p.Status, p.ReviewedAt)
	}
	var w tool.ProposedWrite
	if err := json.Unmarshal(p.ProposedWrite, &w); err != nil {
		t.Fatalf("proposed_write jsonb: %v (%s)", err, p.ProposedWrite)
	}
	want := tool.ProposedWrite{
		V: 1, Kind: "fact", NodeID: nodeID.String(),
		Subject: "Bartholomew", // handler OVERWROTE the LLM subject with the own Node's name
		Fact:    "I brew the finest ale in the realm",
	}
	if w != want {
		t.Errorf("proposed_write = %+v, want %+v", w, want)
	}

	// The KG canon is untouched: the fact only lives as a proposal.
	facts, err := adapter.OwnNodeFacts(ctx, agentID.String())
	if err != nil {
		t.Fatalf("OwnNodeFacts: %v", err)
	}
	for _, f := range facts {
		if f.Body == want.Fact {
			t.Errorf("proposal leaked into KG canon: %+v", f)
		}
	}

	// Deleting the authoring Agent cascades the proposal (ON DELETE CASCADE).
	if _, err := pool.Exec(ctx, `DELETE FROM agents WHERE id = $1`, agentID); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM knowledge_proposal WHERE campaign_id = $1`, campaignID).Scan(&remaining); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if remaining != 0 {
		t.Errorf("agent DELETE did not cascade proposals: %d remain", remaining)
	}
}

// TestRememberKnowledge_DoubleRememberOneRow_RealDB closes the fake-mirror
// seam-drift gap for the #411 write-time dedup: it drives the full
// handler→adapter→real Postgres path twice with the same fact and asserts exactly
// ONE proposal row survives — the second call's ExistingKnowledge reads the first
// (now-persisted) proposal back from the real store and suppresses the repeat.
func TestRememberKnowledge_DoubleRememberOneRow_RealDB(t *testing.T) {
	dsn := startPostgres(t)
	store, _, campaignID, agentID, _ := seedProposalWorld(t, dsn)
	ctx := session.NewContext(context.Background(), session.Identity{CampaignID: campaignID})

	adapter := knowledge.New(store, store.PromptKG())
	rk := tool.NewRememberKnowledge(adapter)
	callCtx := tool.WithCaller(ctx, agentID.String())
	args := json.RawMessage(`{"kind":"fact","fact":"I brew the finest ale in the realm"}`)
	cfg := json.RawMessage(`{"scope":"own_node"}`)

	if _, err := rk.Execute(callCtx, args, cfg); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	// A reworded/re-cased repeat of the SAME fact — the normalized guard catches it.
	repeat := json.RawMessage(`{"kind":"fact","fact":"I brew THE finest ale in the realm!"}`)
	out, err := rk.Execute(callCtx, repeat, cfg)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "already") {
		t.Errorf("second call did not report already-noted: %q", out)
	}

	pending, err := store.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListPendingKnowledgeProposals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("double-remember persisted %d rows, want exactly 1", len(pending))
	}
}
