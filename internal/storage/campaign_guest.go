package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Campaign Guests (ADR-0065): people a GM lets into ONE Campaign with an
// explicit set of Guest Rights, the campaign invitation links that create
// them, and the per-request workspace resolution that decides whether a
// session acts in the user's own Tenant or as a guest.
//
// Rights travel through this file as the stored strings: the vocabulary, its
// implications and its canonical order live in internal/auth (which imports
// this package, not the other way round). Callers write normalized sets and
// normalize again on read, so an unknown string here grants nothing.
//
// ARCHIVED CAMPAIGNS ARE NOT REACHABLE FOR GUESTS. Archiving is the GM putting
// a Campaign away; every guest-facing read below (workspace resolution, the
// workspace list, the session selection, redemption) skips an archived
// Campaign. The campaign_guest rows are kept, so unarchiving restores every
// guest exactly as they were.
//
// NEITHER IS A CAMPAIGN WHOSE HOST LOST THEIR STANDING. The same reads apply
// hostStandingClause: a Campaign of a Tenant whose operator is suspended — or,
// on an `allowlist` deployment, is neither a Platform Admin nor an Access
// Grant holder — is not reachable for its guests and its links do not redeem.
// Again the rows are kept, so restoring the operator restores the guests.

// ErrGuestIsOperator refuses making a Tenant's own operator a Campaign Guest
// of one of that Tenant's Campaigns: they already hold everything a guest row
// could grant, and the row would only shadow their own workspace.
var ErrGuestIsOperator = errors.New("storage: the tenant's operator cannot be a campaign guest")

// CampaignGuest is one guest of a Campaign as its GM sees them.
type CampaignGuest struct {
	CampaignID    uuid.UUID
	UserID        uuid.UUID
	DiscordUserID string
	// Name and Avatar are empty until the person has signed in once (a guest
	// added by snowflake gets a placeholder users row).
	Name   string
	Avatar string
	Rights []string
	// AddedAt is when the guest row was created.
	AddedAt time.Time
	// HasSignedIn is false for a guest added by snowflake who has never
	// completed a sign-in.
	HasSignedIn bool
}

// userHasSignedIn is the SQL test for "this users row is a real sign-in, not a
// placeholder": every OAuth login writes the Discord display name, which is
// never empty, while [Store.AddCampaignGuest] inserts a placeholder with ”.
const userHasSignedIn = `u.name <> ''`

const campaignGuestColumns = `cg.campaign_id, cg.user_id, u.discord_user_id, u.name, u.avatar,
	cg.rights, cg.created_at, ` + userHasSignedIn

func scanCampaignGuest(row pgx.Row) (CampaignGuest, error) {
	var g CampaignGuest
	err := row.Scan(&g.CampaignID, &g.UserID, &g.DiscordUserID, &g.Name, &g.Avatar,
		&g.Rights, &g.AddedAt, &g.HasSignedIn)
	return g, err
}

func nonNilRights(rights []string) []string {
	if rights == nil {
		return []string{}
	}
	return rights
}

// AddCampaignGuest makes a Discord User a guest of the Campaign with exactly
// rights (a GM action). The person need not have signed in yet: a users row
// is created with an empty name, which their first sign-in fills from Discord
// — an EXISTING row's name and avatar are left alone. Adding someone who is
// already a guest replaces their rights. The operator of the Campaign's own
// Tenant is refused with [ErrGuestIsOperator]; an unknown campaign is
// ErrNotFound.
//
// Invitation refusals the person carries from an earlier removal are left
// alone (see refuseInvitations): they are inert while this guest row exists,
// and must hold again if the guest is removed.
func (s *Store) AddCampaignGuest(ctx context.Context, campaignID uuid.UUID, discordUserID string, rights []string, addedBy uuid.UUID) (CampaignGuest, error) {
	discordUserID = strings.TrimSpace(discordUserID)
	if discordUserID == "" {
		return CampaignGuest{}, errors.New("storage: add campaign guest: empty discord user id")
	}
	var g CampaignGuest
	err := s.InTx(ctx, func(tx *Store) error {
		// The no-op DO UPDATE makes RETURNING yield the existing row's id
		// without touching its display fields.
		var userID uuid.UUID
		if err := tx.db.QueryRow(ctx,
			`INSERT INTO users (discord_user_id, name, avatar) VALUES ($1, '', '')
			 ON CONFLICT (discord_user_id) DO UPDATE SET discord_user_id = users.discord_user_id
			 RETURNING id`, discordUserID).Scan(&userID); err != nil {
			return fmt.Errorf("storage: add campaign guest: upsert user %q: %w", discordUserID, err)
		}
		var operator uuid.NullUUID
		err := tx.db.QueryRow(ctx,
			`SELECT t.operator_user_id FROM campaign c JOIN tenant t ON t.id = c.tenant_id
			  WHERE c.id = $1`, campaignID).Scan(&operator)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: add campaign guest: load campaign %s: %w", campaignID, err)
		}
		if operator.Valid && operator.UUID == userID {
			return ErrGuestIsOperator
		}
		if _, err := tx.db.Exec(ctx,
			`INSERT INTO campaign_guest (campaign_id, user_id, rights, added_by)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (campaign_id, user_id) DO UPDATE
			   SET rights = EXCLUDED.rights, updated_at = now()`,
			campaignID, userID, nonNilRights(rights), addedBy); err != nil {
			return fmt.Errorf("storage: add campaign guest: %w", err)
		}
		g, err = tx.GetCampaignGuest(ctx, campaignID, userID)
		return err
	})
	if err != nil {
		return CampaignGuest{}, err
	}
	return g, nil
}

// GetCampaignGuest loads one guest of the Campaign, or ErrNotFound. A user who
// is a guest of a DIFFERENT Campaign reads as absent.
func (s *Store) GetCampaignGuest(ctx context.Context, campaignID, userID uuid.UUID) (CampaignGuest, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+campaignGuestColumns+`
		   FROM campaign_guest cg JOIN users u ON u.id = cg.user_id
		  WHERE cg.campaign_id = $1 AND cg.user_id = $2`, campaignID, userID)
	g, err := scanCampaignGuest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignGuest{}, ErrNotFound
	}
	if err != nil {
		return CampaignGuest{}, fmt.Errorf("storage: get campaign guest: %w", err)
	}
	return g, nil
}

// ListCampaignGuests returns the Campaign's guests, oldest first.
func (s *Store) ListCampaignGuests(ctx context.Context, campaignID uuid.UUID) ([]CampaignGuest, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+campaignGuestColumns+`
		   FROM campaign_guest cg JOIN users u ON u.id = cg.user_id
		  WHERE cg.campaign_id = $1
		  ORDER BY cg.created_at, cg.id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list campaign guests of %s: %w", campaignID, err)
	}
	defer rows.Close()
	var out []CampaignGuest
	for rows.Next() {
		g, err := scanCampaignGuest(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan campaign guest: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list campaign guests of %s: %w", campaignID, err)
	}
	return out, nil
}

// UpdateCampaignGuest replaces a guest's rights. An empty set is allowed — the
// guest keeps the Campaign in their list and can open nothing in it.
// ErrNotFound when the user is not a guest of THIS Campaign.
func (s *Store) UpdateCampaignGuest(ctx context.Context, campaignID, userID uuid.UUID, rights []string) (CampaignGuest, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE campaign_guest SET rights = $3, updated_at = now()
		  WHERE campaign_id = $1 AND user_id = $2`, campaignID, userID, nonNilRights(rights))
	if err != nil {
		return CampaignGuest{}, fmt.Errorf("storage: update campaign guest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return CampaignGuest{}, ErrNotFound
	}
	return s.GetCampaignGuest(ctx, campaignID, userID)
}

// RemoveCampaignGuest ends a guest's access to the Campaign. It takes effect
// on their next request: the workspace is re-resolved every time, and the row
// it joins against is gone. The session selection pointing at the Campaign is
// cleared in the same transaction so the session falls back cleanly instead of
// carrying a dead pointer. ErrNotFound when the user is not a guest of THIS
// Campaign.
//
// The removed person is additionally REFUSED every invitation link of this
// Campaign that exists at this moment (refuseInvitations), however they became
// a guest: a multi-use link stays live for everyone else, and without the
// refusal the removed person would undo the removal by opening it — with the
// link's rights, which may be wider than what the GM gave them. A link created
// afterwards, or adding them by Discord User ID, still lets them back in.
func (s *Store) RemoveCampaignGuest(ctx context.Context, campaignID, userID uuid.UUID) error {
	return s.InTx(ctx, func(tx *Store) error {
		var discordUserID string
		err := tx.db.QueryRow(ctx,
			`DELETE FROM campaign_guest cg USING users u
			  WHERE cg.campaign_id = $1 AND cg.user_id = $2 AND u.id = cg.user_id
			 RETURNING u.discord_user_id`, campaignID, userID).
			Scan(&discordUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: remove campaign guest: %w", err)
		}
		if err := tx.refuseInvitations(ctx, discordUserID, &campaignID); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE sessions SET guest_campaign_id = NULL
			  WHERE user_id = $2 AND guest_campaign_id = $1`, campaignID, userID); err != nil {
			return fmt.Errorf("storage: clear removed guest's session selection: %w", err)
		}
		return nil
	})
}

// ListCampaignInvitations returns the Campaign's `campaign` invitation links,
// newest first — revoked and spent ones included, so the GM sees what happened
// to a link.
func (s *Store) ListCampaignInvitations(ctx context.Context, campaignID uuid.UUID) ([]Invitation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+invitationColumns+` FROM invitation
		  WHERE kind = 'campaign' AND campaign_id = $1
		  ORDER BY created_at DESC, id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list campaign invitations of %s: %w", campaignID, err)
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan campaign invitation: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list campaign invitations of %s: %w", campaignID, err)
	}
	return out, nil
}

// RevokeCampaignInvitation marks one of the Campaign's links revoked. A link
// of another Campaign — or an access invitation — is ErrNotFound: a GM can
// only revoke what belongs to the Campaign they are managing. Guests the link
// already admitted stay.
func (s *Store) RevokeCampaignInvitation(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE invitation SET revoked_at = COALESCE(revoked_at, now())
		  WHERE id = $1 AND kind = 'campaign' AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: revoke campaign invitation %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CampaignRedemption is the input to [Store.RedeemCampaignInvitation]: the
// Discord identity that completed OAuth, and when this round trip acknowledged
// the Nutzungsbedingungen (zero = it did not, and nothing is recorded).
type CampaignRedemption struct {
	User          UpsertUserParams
	AUPAcceptedAt time.Time
}

// CampaignRedeemed is what a redemption produced.
type CampaignRedeemed struct {
	User       User
	CampaignID uuid.UUID
	// Rights is the guest's resulting set.
	Rights []string
	// Changed reports whether the redemption created the guest row — which is
	// exactly when a use of the link was counted.
	Changed bool
}

// RedeemCampaignInvitation turns a `campaign` invitation into a Campaign Guest
// row for the redeemer, atomically — the invitation row is locked, validated
// and its use counted in the same transaction as the guest write, so two
// redeemers can never share the last use ([Store.RedeemAccessInvitation]'s
// discipline). It writes the users row from the redeemer's Discord identity
// (this IS a sign-in) and NOTHING else: no Tenant, no Plan, no session.
//
// Someone who ALREADY is a guest of the Campaign keeps exactly the rights they
// have: the redemption is a sign-in that counts no use, and succeeds even on a
// link that has since been spent, expired or revoked — they are reopening the
// link that let them in. It never widens or resets their rights: what a guest
// holds is whatever their GM last decided (UpdateCampaignGuest), and a link
// they can still reach must not be a way to undo a narrowing. The one thing it
// may write: a guest whose row was made by the GM adding their Discord User ID
// and who opens a link that WOULD have admitted them (live, not pinned to
// someone else, not refused to them) has the row linked to that invitation —
// they have now chosen to be a guest ([GuestStanding] Joined), which the add
// alone could not tell.
//
// Refusals, none of which writes anything:
//   - [ErrUserSuspended] for a suspended redeemer;
//   - [ErrInvitationInvalid] for everything else — unknown token, an access
//     invitation, dead link, pinned to someone else, an archived Campaign, a
//     Campaign whose host lost their standing (hostStandingClause), the
//     redeemer being the operator of the Campaign's own Tenant (who gains
//     nothing from a guest row, and must not be admitted through one), or a
//     redeemer this link was refused to because their GM removed them while
//     the link existed (refuseInvitations).
//
// The invitation is locked FOR NO KEY UPDATE for the reason given at
// [Store.RedeemAccessInvitation]: [Store.RemoveCampaignGuest] deletes the
// guest row and then inserts refusals referencing the Campaign's invitations,
// which under FOR UPDATE deadlocks against a redemption holding the invitation
// and waiting for that guest row.
func (s *Store) RedeemCampaignInvitation(ctx context.Context, token string, p CampaignRedemption) (CampaignRedeemed, error) {
	var res CampaignRedeemed
	err := s.InTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`SELECT `+invitationColumns+` FROM invitation
			  WHERE token_hash = $1 AND kind = 'campaign' FOR NO KEY UPDATE`, sessionTokenHash(token))
		inv, err := scanInvitation(row)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && inv.CampaignID == nil) {
			return ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("storage: lock campaign invitation: %w", err)
		}
		campaignID := *inv.CampaignID

		// Who is redeeming, as far as the database already knows them.
		var (
			existingID  uuid.NullUUID
			suspendedAt *time.Time
		)
		err = tx.db.QueryRow(ctx,
			// FOR NO KEY UPDATE: take the users row before the guest row, the
			// order AddCampaignGuest uses, so the two cannot deadlock.
			`SELECT id, suspended_at FROM users WHERE discord_user_id = $1 FOR NO KEY UPDATE`, p.User.DiscordUserID).
			Scan(&existingID, &suspendedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("storage: redeem campaign invitation: load user: %w", err)
		}
		if suspendedAt != nil {
			return ErrUserSuspended
		}

		var (
			operator     uuid.NullUUID
			archived     *time.Time
			hostStanding bool
		)
		needGrant, admins, recorded := tx.accessArgs()
		err = tx.db.QueryRow(ctx,
			`SELECT t.operator_user_id, c.archived_at, `+hostStandingClause("t", 2)+`
			   FROM campaign c JOIN tenant t ON t.id = c.tenant_id
			  WHERE c.id = $1`, campaignID, needGrant, admins, recorded).Scan(&operator, &archived, &hostStanding)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (archived != nil || !hostStanding)) {
			return ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("storage: redeem campaign invitation: load campaign: %w", err)
		}
		if existingID.Valid && operator.Valid && operator.UUID == existingID.UUID {
			return ErrInvitationInvalid
		}

		var (
			oldRights []string
			isGuest   bool
		)
		if existingID.Valid {
			err = tx.db.QueryRow(ctx,
				`SELECT rights FROM campaign_guest
				  WHERE campaign_id = $1 AND user_id = $2 FOR UPDATE`, campaignID, existingID.UUID).
				Scan(&oldRights)
			switch {
			case err == nil:
				isGuest = true
			case !errors.Is(err, pgx.ErrNoRows):
				return fmt.Errorf("storage: redeem campaign invitation: load guest: %w", err)
			}
		}
		// admits: would this link admit this person, were they not a guest
		// already? A newcomer needs it; for an existing guest it only decides
		// whether opening the link counts as having joined.
		admits := inv.Live(time.Now()) &&
			(inv.DiscordUserID == "" || inv.DiscordUserID == p.User.DiscordUserID)
		if admits {
			refused, err := tx.invitationRefused(ctx, inv.ID, p.User.DiscordUserID)
			if err != nil {
				return err
			}
			admits = !refused
		}
		if !isGuest && !admits {
			return ErrInvitationInvalid
		}

		u, err := tx.UpsertUser(ctx, p.User)
		if err != nil {
			return err
		}
		if !p.AUPAcceptedAt.IsZero() {
			if _, err := tx.db.Exec(ctx,
				`UPDATE users SET aup_accepted_at = $2, updated_at = now() WHERE id = $1`,
				u.ID, p.AUPAcceptedAt); err != nil {
				return fmt.Errorf("storage: record AUP acceptance: %w", err)
			}
			u.AUPAcceptedAt = &p.AUPAcceptedAt
		}
		res = CampaignRedeemed{User: u, CampaignID: campaignID, Rights: nonNilRights(oldRights)}
		if isGuest {
			if admits {
				if _, err := tx.db.Exec(ctx,
					`UPDATE campaign_guest SET invitation_id = $3
					  WHERE campaign_id = $1 AND user_id = $2 AND invitation_id IS NULL`,
					campaignID, u.ID, inv.ID); err != nil {
					return fmt.Errorf("storage: redeem campaign invitation: link added guest: %w", err)
				}
			}
			return nil
		}

		// ON CONFLICT DO NOTHING covers the race the invitation lock does not:
		// the same person redeeming two DIFFERENT links of one Campaign at once
		// (or the GM adding them mid-redeem). Both writers serialize on the
		// unique index; the later one inserts nothing and is then exactly the
		// "already a guest" case above — it keeps the rights the earlier one
		// committed and counts no use.
		err = tx.db.QueryRow(ctx,
			`INSERT INTO campaign_guest (campaign_id, user_id, rights, added_by, invitation_id)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (campaign_id, user_id) DO NOTHING
			 RETURNING rights`,
			campaignID, u.ID, nonNilRights(inv.Rights), inv.CreatedBy, inv.ID).Scan(&res.Rights)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.db.QueryRow(ctx,
				`SELECT rights FROM campaign_guest WHERE campaign_id = $1 AND user_id = $2`,
				campaignID, u.ID).Scan(&res.Rights); err != nil {
				return fmt.Errorf("storage: redeem campaign invitation: reload guest: %w", err)
			}
			res.Rights = nonNilRights(res.Rights)
			return nil
		}
		if err != nil {
			return fmt.Errorf("storage: write guest from invitation: %w", err)
		}
		res.Changed = true
		if _, err := tx.db.Exec(ctx, `UPDATE invitation SET uses = uses + 1 WHERE id = $1`, inv.ID); err != nil {
			return fmt.Errorf("storage: count invitation use: %w", err)
		}
		return nil
	})
	if err != nil {
		return CampaignRedeemed{}, err
	}
	return res, nil
}

// Workspace is what one request acts in (ADR-0065): the user's own Tenant, or
// — when Guest — one Campaign of a HOST Tenant with the rights the guest row
// carries.
type Workspace struct {
	// TenantID is the request's Tenant: the user's own, or the host's.
	TenantID uuid.UUID
	// Guest is true when the session acts as a Campaign Guest.
	Guest bool
	// CampaignID and Rights are set only for a guest workspace.
	CampaignID uuid.UUID
	Rights     []string
}

// ResolveWorkspace decides, in ONE query, what the session acts as. The first
// that applies wins:
//
//  1. the guest Campaign selected on the session (sessions.guest_campaign_id),
//     if the user still is a guest of it — the selection is never an
//     authorization, the campaign_guest join is;
//  2. the user's own Tenant;
//  3. the guest Campaign they were most recently added to;
//  4. ErrNotFound.
//
// ownTenantNeedsGrant makes step 2 additionally require an Access Grant. The
// auth tier passes true on an `allowlist` deployment for anyone who is not a
// Platform Admin: there, operating a Tenant is granted by a Platform Admin and
// revoked by deleting the grant, and since guests are admitted on every
// deployment a sign-in alone no longer proves the grant still exists. Without
// this, a GM whose access was revoked — but who is someone's Campaign Guest —
// would sign in as a guest and land back in their own Tenant.
//
// Archived Campaigns never resolve (see the file comment), nor Campaigns whose
// host lost their standing (steps 1 and 3 apply hostStandingClause), and
// neither does a guest row on a Campaign of a Tenant the user operates
// themselves.
func (s *Store) ResolveWorkspace(ctx context.Context, userID uuid.UUID, sessionToken string, ownTenantNeedsGrant bool) (Workspace, error) {
	var (
		w        Workspace
		campaign uuid.NullUUID
	)
	hostNeedsGrant, admins, recorded := s.accessArgs()
	// ord sorts within a step: the OLDEST own Tenant first (TenantForUser's
	// order), the NEWEST guest row first.
	err := s.db.QueryRow(ctx,
		`SELECT ws.tenant_id, ws.campaign_id, ws.rights
		   FROM (
		         SELECT 1 AS step, c.tenant_id, c.id AS campaign_id, cg.rights,
		                0::float8 AS ord, cg.id AS tie
		           FROM sessions se
		           JOIN campaign_guest cg ON cg.campaign_id = se.guest_campaign_id AND cg.user_id = se.user_id
		           JOIN campaign c ON c.id = cg.campaign_id
		           JOIN tenant t ON t.id = c.tenant_id
		          WHERE se.token = $2 AND se.user_id = $1
		            AND c.archived_at IS NULL
		            AND t.operator_user_id IS DISTINCT FROM $1
		            AND `+hostStandingClause("t", 4)+`
		         UNION ALL
		         SELECT 2, t.id, NULL::uuid, NULL::text[],
		                extract(epoch FROM t.created_at)::float8, t.id
		           FROM tenant t
		          WHERE t.operator_user_id = $1 AND `+ownTenantGrantClause+`
		         UNION ALL
		         SELECT 3, c.tenant_id, c.id, cg.rights,
		                -extract(epoch FROM cg.created_at)::float8, cg.id
		           FROM campaign_guest cg
		           JOIN campaign c ON c.id = cg.campaign_id
		           JOIN tenant t ON t.id = c.tenant_id
		          WHERE cg.user_id = $1
		            AND c.archived_at IS NULL
		            AND t.operator_user_id IS DISTINCT FROM $1
		            AND `+hostStandingClause("t", 4)+`
		        ) ws
		  ORDER BY ws.step, ws.ord, ws.tie
		  LIMIT 1`, userID, sessionTokenHash(sessionToken), ownTenantNeedsGrant, hostNeedsGrant, admins, recorded).
		Scan(&w.TenantID, &campaign, &w.Rights)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("storage: resolve workspace of user %s: %w", userID, err)
	}
	if campaign.Valid {
		w.Guest = true
		w.CampaignID = campaign.UUID
	}
	w.Rights = nonNilRights(w.Rights)
	return w, nil
}

// ownTenantGrantClause is the "may this user act in a Tenant they operate"
// test shared by [Store.ResolveWorkspace] and [Store.OwnTenantForUser]: $1 is
// the user id, $3 whether an Access Grant is required.
const ownTenantGrantClause = `(NOT $3::boolean OR EXISTS (
		SELECT 1 FROM access_grant g JOIN users gu ON gu.discord_user_id = g.discord_user_id
		 WHERE gu.id = $1))`

// ownTenantGrantClauseTwoArgs is [ownTenantGrantClause] for a query whose
// grant flag is $2.
const ownTenantGrantClauseTwoArgs = `(NOT $2::boolean OR EXISTS (
		SELECT 1 FROM access_grant g JOIN users gu ON gu.discord_user_id = g.discord_user_id
		 WHERE gu.id = $1))`

// OwnTenantForUser is [Store.TenantForUser] under the same rule step 2 of
// [Store.ResolveWorkspace] applies: the Tenant the user operates AND may act
// in. ErrNotFound when they operate none, or when needsGrant is set and they
// hold no Access Grant.
func (s *Store) OwnTenantForUser(ctx context.Context, userID uuid.UUID, needsGrant bool) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT t.id FROM tenant t
		  WHERE t.operator_user_id = $1 AND `+ownTenantGrantClauseTwoArgs+`
		  ORDER BY t.created_at, t.id LIMIT 1`, userID, needsGrant).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: own tenant of user %s: %w", userID, err)
	}
	return id, nil
}

// SetSessionWorkspace records what the session acts as. A nil campaignID
// clears the selection (the user's own Tenant, or — having none — whatever
// [Store.ResolveWorkspace] falls back to). A campaign is only ever selected
// when the user is a guest of it right now, it is not archived and its host is
// in good standing (hostStandingClause); anything
// else is ErrNotFound and leaves the session untouched. The selection is a
// convenience, never the authorization: every request re-joins it against
// campaign_guest.
func (s *Store) SetSessionWorkspace(ctx context.Context, userID uuid.UUID, sessionToken string, campaignID *uuid.UUID) error {
	query := `UPDATE sessions SET guest_campaign_id = NULL WHERE token = $2 AND user_id = $1`
	args := []any{userID, sessionTokenHash(sessionToken)}
	if campaignID != nil {
		query = `UPDATE sessions SET guest_campaign_id = $3
			  WHERE token = $2 AND user_id = $1
			    AND EXISTS (
			          SELECT 1 FROM campaign_guest cg
			            JOIN campaign c ON c.id = cg.campaign_id
			            JOIN tenant t ON t.id = c.tenant_id
			           WHERE cg.user_id = $1 AND cg.campaign_id = $3
			             AND c.archived_at IS NULL
			             AND t.operator_user_id IS DISTINCT FROM $1
			             AND ` + hostStandingClause("t", 4) + `)`
		needGrant, admins, recorded := s.accessArgs()
		args = append(args, *campaignID, needGrant, admins, recorded)
	}
	tag, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("storage: set session workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GuestWorkspace is one Campaign a user is a Campaign Guest of, as THEY see
// it: the workspace switcher's "shared with me" entry.
type GuestWorkspace struct {
	CampaignID   uuid.UUID
	CampaignName string
	// TenantName is the host Tenant's display name.
	TenantName string
	Rights     []string
	AddedAt    time.Time
}

// ListGuestWorkspaces returns every Campaign the user can act in as a guest,
// most recently added first — the same set [Store.ResolveWorkspace] would
// resolve (archived Campaigns, Campaigns whose host lost their standing and
// Campaigns of a Tenant the user operates are absent).
func (s *Store) ListGuestWorkspaces(ctx context.Context, userID uuid.UUID) ([]GuestWorkspace, error) {
	needGrant, admins, recorded := s.accessArgs()
	rows, err := s.db.Query(ctx,
		`SELECT c.id, c.name, t.name, cg.rights, cg.created_at
		   FROM campaign_guest cg
		   JOIN campaign c ON c.id = cg.campaign_id
		   JOIN tenant t ON t.id = c.tenant_id
		  WHERE cg.user_id = $1
		    AND c.archived_at IS NULL
		    AND t.operator_user_id IS DISTINCT FROM $1
		    AND `+hostStandingClause("t", 2)+`
		  ORDER BY cg.created_at DESC, cg.id`, userID, needGrant, admins, recorded)
	if err != nil {
		return nil, fmt.Errorf("storage: list guest workspaces of user %s: %w", userID, err)
	}
	defer rows.Close()
	var out []GuestWorkspace
	for rows.Next() {
		var w GuestWorkspace
		if err := rows.Scan(&w.CampaignID, &w.CampaignName, &w.TenantName, &w.Rights, &w.AddedAt); err != nil {
			return nil, fmt.Errorf("storage: scan guest workspace: %w", err)
		}
		w.Rights = nonNilRights(w.Rights)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list guest workspaces of user %s: %w", userID, err)
	}
	return out, nil
}

// GuestStanding is what the OAuth callback needs to know about a Discord User
// who presented no invitation: are they a Campaign Guest anywhere, and do they
// operate a Tenant.
type GuestStanding struct {
	// CampaignID is the guest Campaign they were most recently added to.
	CampaignID uuid.UUID
	// OperatesTenant is true when they also operate a Tenant of their own.
	OperatesTenant bool
	// Joined is true when at least one of their reachable guest rows came
	// through an invitation link — they CHOSE to be a guest. False means every
	// row was written by a GM adding their Discord User ID, which needs no
	// consent and may have happened before they ever signed in. `open`
	// admission tells the two apart: only a guest who joined signs in as a
	// guest instead of self-signing-up, otherwise any GM could keep any
	// Discord User from ever founding a Tenant by adding their id.
	Joined bool
}

// GuestStandingOf reports a Discord User's standing as a Campaign Guest, or
// ErrNotFound when they are a guest of no reachable Campaign (archived ones
// and ones whose host lost their standing do not count — see the file
// comment) or have no users row at all.
func (s *Store) GuestStandingOf(ctx context.Context, discordUserID string) (GuestStanding, error) {
	var st GuestStanding
	needGrant, admins, recorded := s.accessArgs()
	err := s.db.QueryRow(ctx,
		`SELECT c.id, EXISTS (SELECT 1 FROM tenant own WHERE own.operator_user_id = u.id),
		        bool_or(cg.invitation_id IS NOT NULL) OVER ()
		   FROM users u
		   JOIN campaign_guest cg ON cg.user_id = u.id
		   JOIN campaign c ON c.id = cg.campaign_id
		   JOIN tenant t ON t.id = c.tenant_id
		  WHERE u.discord_user_id = $1
		    AND c.archived_at IS NULL
		    AND t.operator_user_id IS DISTINCT FROM u.id
		    AND `+hostStandingClause("t", 2)+`
		  ORDER BY cg.created_at DESC, cg.id
		  LIMIT 1`, discordUserID, needGrant, admins, recorded).Scan(&st.CampaignID, &st.OperatesTenant, &st.Joined)
	if errors.Is(err, pgx.ErrNoRows) {
		return GuestStanding{}, ErrNotFound
	}
	if err != nil {
		return GuestStanding{}, fmt.Errorf("storage: guest standing of %q: %w", discordUserID, err)
	}
	return st, nil
}

// InvitationTarget names what a `campaign` invitation opens, for the public
// join preview: the Campaign and its host Tenant.
type InvitationTarget struct {
	CampaignName string
	TenantName   string
}

// GetInvitationCampaign loads the display names behind a campaign invitation's
// campaign id. An archived Campaign — or one whose host lost their standing
// (hostStandingClause) — is ErrNotFound: its links cannot be redeemed, so
// they must not preview as working.
func (s *Store) GetInvitationCampaign(ctx context.Context, campaignID uuid.UUID) (InvitationTarget, error) {
	var ic InvitationTarget
	needGrant, admins, recorded := s.accessArgs()
	err := s.db.QueryRow(ctx,
		`SELECT c.name, t.name FROM campaign c JOIN tenant t ON t.id = c.tenant_id
		  WHERE c.id = $1 AND c.archived_at IS NULL
		    AND `+hostStandingClause("t", 2), campaignID, needGrant, admins, recorded).Scan(&ic.CampaignName, &ic.TenantName)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvitationTarget{}, ErrNotFound
	}
	if err != nil {
		return InvitationTarget{}, fmt.Errorf("storage: invitation campaign %s: %w", campaignID, err)
	}
	return ic, nil
}

// VoiceSessionInCampaign reports whether the Voice Session belongs to the
// Campaign. It is [Store.VoiceSessionInTenant] one level narrower, for a
// Campaign Guest: a session of ANOTHER Campaign of the same host Tenant must
// read as absent.
func (s *Store) VoiceSessionInCampaign(ctx context.Context, campaignID, sessionID uuid.UUID) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx,
		`SELECT 1 FROM voice_sessions WHERE id = $1 AND campaign_id = $2`, sessionID, campaignID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: voice session %s in campaign %s: %w", sessionID, campaignID, err)
	}
	return true, nil
}

// PrivateNodeIDs returns the ids of the Campaign's gm_private Nodes — the set
// a reader without the wiki.secrets Guest Right must not learn anything about.
// Handlers use it to drop what merely POINTS at a withheld Node (an Edge, a
// tag, a board entry, a Pin): knowing "something is connected here" and what
// it is called is most of a secret.
func (s *Store) PrivateNodeIDs(ctx context.Context, campaignID uuid.UUID) (map[uuid.UUID]struct{}, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id FROM kg_node WHERE campaign_id = $1 AND gm_private`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: private node ids of %s: %w", campaignID, err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan private node id: %w", err)
		}
		out[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: private node ids of %s: %w", campaignID, err)
	}
	return out, nil
}
