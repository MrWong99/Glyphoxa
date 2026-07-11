-- +goose Up

-- Knowledge Proposals (ADR-0052, #300): an Agent's remember_knowledge call
-- records a PENDING proposal here — nothing touches kg_node/kg_edge until the GM
-- approves it in the review surface (PR-b). This is the KG's v1 trust model: an
-- Agent can only ever SUGGEST canon, never write it. The proposed_write jsonb is
-- the versioned tagged union pkg/tool.ProposedWrite marshals ("v":1; kind
-- fact/edge/node); it is stored verbatim and interpreted only at approval time.
--
-- No Butler grant is seeded here: the write authority is opt-in per Agent via the
-- grant editor's scope (ADR-0029), never a table-level default. ON DELETE CASCADE
-- ties a proposal to both its Campaign and its authoring Agent so deleting either
-- reaps the queue with no cleanup code.
--
-- NOTE (merge order): this migration is numbered 00026 and assumes 00024/00025
-- (in-flight PRs) land first; renumber on rebase if they do not.

CREATE TYPE knowledge_proposal_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE knowledge_proposal (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id        uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    authoring_agent_id uuid NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    proposed_write     jsonb NOT NULL,
    status             knowledge_proposal_status NOT NULL DEFAULT 'pending',
    created_at         timestamptz NOT NULL DEFAULT now(),
    reviewed_at        timestamptz
);

-- The review surface (PR-b) lists a Campaign's pending queue oldest-first; a
-- partial index keeps that read cheap and skips already-reviewed rows.
CREATE INDEX knowledge_proposal_pending_idx
    ON knowledge_proposal (campaign_id, created_at)
    WHERE status = 'pending';

-- +goose Down

DROP TABLE knowledge_proposal;
DROP TYPE knowledge_proposal_status;
