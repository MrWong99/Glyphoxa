# Players are not tenant members (Discord-identity default)

Players are not Tenant Members; they are scoped via the Characters they play. Each Character carries `discord_user_id` (mandatory) and `linked_user_id` (nullable, set on first Discord OAuth). A Player who signs in via Discord OAuth becomes a Linked Player and gains web access scoped to their Characters, without becoming a Member of the Tenant. Tenant membership remains GM-tier (`owner`/`admin`/`gm`).

**Why:** Discord-identity-by-default avoids forcing players to register before they can play. Transcript attribution and Address Detection only need the Discord User ID, which the Bot already has from voice presence.
