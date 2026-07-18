# Player Invitations: Linked Player access at per-link Access Levels; transcript text shared by GM opt-in

The operator wants GMs to invite the people they play with to see a Campaign's
transcripts and Highlights, with capabilities depending on an assigned **Player
Access Level**. Decided with the operator 2026-07-18: those invitees are **the
table's Players**, so this is the Linked Player lane — decided in ADR-0003 and
never built (`character.linked_user_id` exists in schema; no code path writes
it) —
**not** a membership feature. Players do not become Tenant Members and `player`
never enters the Member Role enum (ADR-0002/0003 and CONTEXT.md stand). A
`tenant_members` table stays a separate, future ADR for the day a Tenant needs a
second GM-tier human. Full grounding and the adversarial-review findings live in
`docs/devs/self-signup-and-invitations-design.md`; prerequisites are ADR-0055 and
its hardening epic.

## What this decides

- **The Linked Player lane gets built, entered via a Player Invitation.** A
  Campaign's GM mints a **Player Invitation** for a Character (or campaign-wide);
  the Player opens it, authenticates via the existing Discord OAuth pipeline, and
  acceptance writes `character.linked_user_id` and records the granted level.
  Acceptance mints a cookie auth session like any login — humans never get bearer
  tokens (ADR-0016).
- **`player_invitation` is its own record:** (tenant, campaign, optional
  character, access level, `token_hash`, minted-by, expiry, single-use flag,
  optional pinned `discord_user_id`, accepted-by/at, revoked-at). The token is
  the existing 256-bit `crypto/rand` primitive, stored hashed. Naming and route
  are deliberate: **`/join/<token>`**, never `/invite` — `internal/discordinvite`
  (ADR-0047) and the SPA's `discord.com/invite/{code}` parsing already own
  "invite" for Discord guild invites. Prose term: **Player Invitation** (added to
  CONTEXT.md).
- **The OAuth callback forks on intent.** Found-a-tenant (ADR-0055 signup) vs
  link-a-character (this ADR) are distinct branches selected by the presence of
  an invitation; a linking Player must never traverse signup provisioning — an
  auto-minted Tenant plus default-Plan Subscription per player would pollute what
  ADR-0054 calls the revenue record.
- **Redemption security is part of the decision, not implementation detail:**
  the invitation token rides a separate signed carrier, never the OAuth `state`
  nonce (`state` is the login-CSRF anti-forgery value; a token from a shareable
  link is neither unpredictable nor browser-bound); acceptance is bound to the
  authenticated identity — pin the Player's snowflake at mint (the GM already has
  it from voice presence, ADR-0003) or bind on first authentication and require a
  match on reuse; the single-use claim is atomic (conditional update, no
  validate-then-write race); mint and revoke are GM actions on a Campaign the GM
  runs.
- **Admission interplay (decided):** a Player Invitation admits a
  not-otherwise-admitted Discord User only where the deployment allows it —
  always in `open` Admission Mode; on `allowlist` deployments only when the
  deployment operator (the platform-administration list, ADR-0055) explicitly
  enables invitation admission **per Tenant** (default OFF). A GM can never
  unilaterally widen a locked-down self-host's trust boundary (ADR-0041 stands).
  Where linked players are admitted, the boot sweep must not evict them — their
  authorization is checked per-request against their links (the ADR-0055
  re-check machinery), not against the allowlist.
- **Player Access Level** (per link, stored on the invitation/link — a Player
  may hold different levels across Campaigns): `own-character` (their Character's
  own Transcript Lines and the promoted Highlights they appear in),
  `campaign-highlights` (the Campaign's promoted Highlights, no transcript
  text), `campaign-transcripts` (the Campaign's transcript text plus promoted
  Highlights; requires the share toggle below). GM-assigned, GM-revocable;
  revocation and downgrades take effect per-request, with targeted auth-session
  deletion.
- **Sharing semantics under ADR-0051 (decided):** a Linked Player viewing
  content in-instance is **not** "leaving the instance"; the explicit GM actions
  ADR-0051 requires are the invitation itself and a **per-Campaign "share
  transcripts with linked players" opt-in, default OFF** (mirroring the tape
  consent default), which gates the `campaign-transcripts` level. Transcript
  *text* was never consent-gated — ADR-0051 covers audio — so this is new policy
  surface, and the decision is that **the GM opt-in covers the table's text for
  v1**: no per-speaker text consent (text was never E2EE-shaped the way DAVE
  audio is), revisit if a table asks. Players see **promoted Highlights only** —
  never Highlight Candidates (GM-curation stays private, 7-day purge unchanged),
  never Promote/Delete/Share/Recap controls. Campaign Bundle export contains full
  transcripts and remains operator-side only, never a Player capability.
- **Authorization: a Linked Player is a third principal**, beside the operator
  (and future Members), enforced in the single transport-agnostic policy seam on
  both transports (the Connect interceptor stack and the guarded byte mounts —
  the invariant from #446). Every Linked Player read intersects three things:
  their Character links × the Campaign's share toggle × the granted Access
  Level.
- **`plan.limits` gets its first consumer:** `max_linked_players` (per Tenant),
  checked at invitation mint. The limits bag was built for exactly this
  (ADR-0054); no schema churn.

## Considered and rejected

- **A read-only `viewer` Member Role** — wrong audience: it would amend the
  ADR-0002 enum *and* ADR-0003's "membership remains GM-tier" for people who are
  Players, and tenant-wide read overshoots what a table member should see.
- **`tenant_members` as the vehicle** — membership is orthogonal to Player
  sharing; building it here would couple this feature to backfill/cutover work
  (parked in the design note) it doesn't need.
- **Per-speaker consent for transcript text in v1** — the GM opt-in covers the
  table; unlike tape audio there is no E2EE expectation to invert, and per-speaker
  text exclusion would punch holes in a shared record the table already lived.
- **Unpinned, non-expiring share links** — a bearer secret: whoever opens it
  first becomes the Player; identity binding and expiry are mandatory.
- **Invitation-always-admits** — on an `allowlist` deployment this would delegate
  the deployment's only trust boundary to every GM; rejected, operator-enabled
  only.
- **Carrying the invitation token in the OAuth `state` parameter** — destroys the
  login-CSRF property `state` exists for.

## Relationship to other ADRs

- **ADR-0003** — implemented, not amended: Players stay non-Members; this ADR is
  the lane it decided (pointer note added there).
- **ADR-0051** — extended in-instance: its gate question gains the answer "a
  Linked Player sees only what the GM's invitation + share toggle grant, and
  audio-bearing Highlights only post-promotion"; capture consent is untouched
  (relationship note added there).
- **ADR-0055** — admission machinery, per-request re-check, and the intent-forked
  callback this ADR rides on.
- **ADR-0047** — the "invite" namespace stays Discord guild invites; hence
  `/join` and "Player Invitation".
- **ADR-0016** — cookie sessions only; no bearer auth for humans.
- **ADR-0002** — Member Role enum untouched.
- **ADR-0054** — `plan.limits` first consumer; Subscriptions and the revenue
  record are never touched by Player linking.
