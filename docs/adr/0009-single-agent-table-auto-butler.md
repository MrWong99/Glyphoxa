# Single Agent table; Butler auto-created per Campaign

A single `agents` table is polymorphic via an `agent_role` enum (`butler` | `character`), so one unified orchestrator and one address-detection code path handle both. A Postgres partial unique index enforces exactly one Butler per Campaign.

Each Campaign auto-creates its own Butler on creation with hardcoded defaults (name "Glyphoxa", Tool Grants `dice` + `transcript_search` + `rules_lookup`); there is no `tenant_butler_defaults` layer. The GM edits the auto-created Butler post-creation.

Slash command routing resolves Active Campaign in this order:

1. Active Voice Session in this Guild → that Campaign,
2. else `active_campaign_id` on the GM's user profile,
3. else fail with a `/use campaign:<name>` hint.

**Why:** the Butler is a Campaign-scoped concept (per-campaign tools, per-campaign voice). A tenant-default layer would have to be merged in at runtime, complicating routing for no real win. Polymorphism on the Agent table beats two separate tables that would share most columns.
