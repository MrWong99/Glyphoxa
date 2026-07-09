package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotArchived is returned by DeleteCampaign when the target campaign exists
// but is not archived: the hard delete is refused until the campaign has been
// archived first (#269, decided on #265). The RPC layer maps it to Connect
// CodeFailedPrecondition.
var ErrNotArchived = errors.New("storage: campaign not archived")

// ListAllCampaigns returns every Campaign — active AND archived — ordered by name
// (then id for a stable tie-break). It backs the archive-management panel's
// include_archived read (#269); the default list surfaces (ListCampaigns, the
// /glyphoxa use autocomplete) stay archive-excluding. Single-operator today, so
// it is unscoped, mirroring ListCampaigns; tenant scoping fills in behind the
// X-Tenant-Id pass-through later (ADR-0039).
func (s *Store) ListAllCampaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignColumns+` FROM campaign ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list all campaigns: %w", err)
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan campaign: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list all campaigns: %w", err)
	}
	return out, nil
}

// ArchiveCampaign marks a campaign archived and returns the updated row (#269).
// It is idempotent: COALESCE(archived_at, now()) keeps an already-archived
// campaign's original timestamp (the audit trail of WHEN it was first archived),
// so a re-archive is a no-op on the timestamp. In the same transaction it clears
// users.active_campaign_id for every operator whose durable /glyphoxa use
// selection pointed at this campaign — the decided "archived durable selection is
// treated as absent" (#265): the slash surface then falls to its /use hint and
// the web tier to its most-recent fallback, neither of which resolves an archived
// campaign. A missing id yields ErrNotFound.
func (s *Store) ArchiveCampaign(ctx context.Context, id uuid.UUID) (Campaign, error) {
	var c Campaign
	err := s.InTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`UPDATE campaign
			    SET archived_at = COALESCE(archived_at, now()), updated_at = now()
			  WHERE id = $1
			 RETURNING `+campaignColumns, id)
		got, err := scanCampaign(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: archive campaign %s: %w", id, err)
		}
		// Treat an archived durable selection as absent (#265): null every operator
		// pointer at this campaign so resolution falls back cleanly.
		if _, err := tx.db.Exec(ctx,
			`UPDATE users SET active_campaign_id = NULL, updated_at = now()
			  WHERE active_campaign_id = $1`, id); err != nil {
			return fmt.Errorf("storage: clear active selections for archived campaign %s: %w", id, err)
		}
		c = got
		return nil
	})
	if err != nil {
		return Campaign{}, err
	}
	return c, nil
}

// UnarchiveCampaign clears a campaign's archived_at, returning it to the active
// set, and returns the updated row (#269). A missing id yields ErrNotFound.
// Un-archiving does not restore any operator's cleared durable selection (that
// pointer was nulled on archive) — the campaign simply becomes selectable again.
func (s *Store) UnarchiveCampaign(ctx context.Context, id uuid.UUID) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE campaign SET archived_at = NULL, updated_at = now()
		  WHERE id = $1
		 RETURNING `+campaignColumns, id)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: unarchive campaign %s: %w", id, err)
	}
	return c, nil
}

// DeleteCampaign permanently removes an ALREADY-ARCHIVED campaign (#269). The
// single DELETE cascades to everything owned by the campaign — Agents (and their
// Tool Grants), Knowledge Graph Nodes/Edges, Voice Sessions (and their Transcript
// Lines), and Transcript Chunks — via the ON DELETE CASCADE foreign keys the
// schema already declares (00001 agents/transcript_chunk, 00006 voice_sessions,
// 00007 transcript_line, 00010 kg_node, 00012 kg_edge, 00013 tool_agent_grant);
// users.active_campaign_id is nulled via its ON DELETE SET NULL (00014). The
// Butler is removed through the agents CASCADE, deliberately NOT through
// DeleteAgent's butler guard (ADR-0009): a campaign delete takes its Butler with
// it. The WHERE archived_at IS NOT NULL clause makes the delete refuse a
// non-archived campaign; when no row is affected, GetCampaign disambiguates a
// missing campaign (ErrNotFound) from a live one (ErrNotArchived). This is
// irrecoverable removal of play history including transcript PII — no soft-delete
// retention window (#265).
func (s *Store) DeleteCampaign(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM campaign WHERE id = $1 AND archived_at IS NOT NULL`, id)
	if err != nil {
		return fmt.Errorf("storage: delete campaign %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		// Nothing deleted: either the campaign does not exist, or it exists but is
		// not archived. Disambiguate so the RPC layer can map each to its own code.
		if _, gerr := s.GetCampaign(ctx, id); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		} else if gerr != nil {
			return gerr
		}
		return ErrNotArchived
	}
	return nil
}
