# Rollover tape: all-participant consent, bounded retention, GM-gated sharing

Epic 8 persists room audio for the first time. Discord voice is DAVE E2E-encrypted (ADR-0006); the Bot legitimately holds plaintext as an MLS member, but **persisting** audio inverts participants' E2EE expectations, Discord's developer policy requires consent for recording, and self-hosters inherit GDPR exposure. Decided with the operator 2026-07-07 (#303); this ADR gates the entire epic.

## What this decides

- **Consent: every human participant, individually.** A Campaign-level GM opt-in arms the feature (**default OFF; capture is hard-disabled without it**). When a Voice Session starts with the tape armed, the Bot posts an in-channel **disclosure message with consent buttons**. Only speakers who have consented (once per Campaign, revocable) have their Speaker Lanes copied into the tape — **per-speaker exclusion**, enabled by ADR-0050's lanes. No consent, no capture of that speaker; ever, not even transiently.
- **Agent speech is always on tape**: it is synthetic, produced on the outbound playback path (a separate tap from inbound room audio), and carries no personal data of participants.
- **Scope: a 120-second rolling buffer** (in-memory ring per consented lane + agent tap), discarded wholesale at Voice Session end. 120s over 60s: moments need their build-up; the memory cost is trivial.
- **Candidates are ephemeral** (per #305's GM-curation model): detector-flagged clips cut from the tape are blob-backed (ADR-0048) but retained only **until the GM's session-end review, with a 7-day safety auto-purge** for never-reviewed candidates. Unpromoted candidates are deleted on review.
- **Highlights (GM-promoted candidates) persist until the GM deletes them** — no TTL. Deletion cascades through the blob seam; Campaign delete (#265 semantics) removes them with everything else.
- **Sharing posture: nothing leaves the instance without an explicit GM action.** No auto-posting anywhere; the Discord-delivery slice (#310) is share-button-only. Consent covers *capture*; the GM gate covers *distribution*.
- The gate question, answered: **a Player's voice cannot end up in any clip without their explicit consent, and no clip leaves the instance without an explicit GM action.**

## Considered and rejected

- **GM-only consent + disclosure** — informs participants but doesn't ask them; fails the spirit of Discord's developer policy and puts self-hosters on the wrong side of GDPR's consent bar for biometric-adjacent data.
- **All-or-nothing capture** (disable the tape unless everyone consents) — punishes the table for one abstainer; per-lane exclusion makes it unnecessary.
- **TTL on Highlights** — a promoted Highlight is deliberate, GM-owned content like any other Campaign record; deletion is a decision, not a timer.
- **Including unconsented audio transiently and filtering at promotion** — "transient" recordings are still recordings; exclusion must happen at the tape boundary.

## Relationship to other ADRs

ADR-0006 (DAVE — why expectations are E2EE-shaped), ADR-0050 (per-speaker lanes that make exclusion possible), ADR-0048 (blob lifecycle hooks that implement deletion), decisions on #305 (curation model producing candidates) and #310 (delivery obeying the sharing posture). **ADR-0056 (2026-07-18)** extends the sharing posture in-instance: a Linked Player's viewing of promoted Highlights and (GM-opt-in) transcript text is gated by the explicit GM actions of Player Invitation + per-Campaign share toggle; Highlight Candidates stay GM-only, capture consent here is untouched, and transcript *text* visibility — outside this ADR's audio scope — is decided there.
