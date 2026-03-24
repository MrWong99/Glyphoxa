---
title: "Frontend Design — Glyphoxa Web Management Service"
type: feat
status: draft
date: 2026-03-24
depends_on:
  - docs/plans/2026-03-23-admin-web-ui-plan.md
  - docs/plans/web-management/pricing-models.md
---

# Frontend Design — Glyphoxa Web Management Service

## 1. Overview

This document defines the frontend architecture, page designs, component hierarchy,
and interaction patterns for the Glyphoxa Web Management Service — a self-service
portal for Dungeon Masters to manage campaigns, NPCs, sessions, billing, and more.

**Key constraints:**
- **Self-service for non-technical DMs** — no YAML, no CLI, no API keys
- **Separate service** — own Docker container, own domain (`app.glyphoxa.com`)
- **Mobile-friendly** — DMs manage NPCs from their phone during sessions
- **Scale to >1000 users** — multi-tenant, tenant-isolated, performant
- **English MVP** — i18n-ready for German (and beyond) in Phase 2

**Deployment model:** The frontend is a standalone SPA served by its own container
(nginx or Node SSR), communicating with the Glyphoxa Gateway API over HTTPS.
This decouples frontend release cycles from backend deployments and allows
independent scaling, CDN caching, and A/B testing.

```
┌──────────────────────┐         ┌──────────────────────┐
│  app.glyphoxa.com    │  HTTPS  │  api.glyphoxa.com    │
│  ┌────────────────┐  │ ──────→ │  ┌────────────────┐  │
│  │  SPA (Next.js) │  │         │  │  Gateway API   │  │
│  │  Docker: nginx  │  │         │  │  (Go)          │  │
│  └────────────────┘  │         │  └────────────────┘  │
└──────────────────────┘         └──────────────────────┘
         │                                │
         │  CDN (Cloudflare)              │  PostgreSQL + Vault
         │  Static assets cached          │  Per-tenant schemas
```

---

## 2. Tech Stack

### Framework: Next.js 15 (App Router)

| Choice | Rationale |
|--------|-----------|
| **Next.js 15** | SSR for landing/marketing pages (SEO), SPA behavior for dashboard. App Router enables React Server Components for fast initial loads. Standalone output mode for Docker. |
| **React 19** | Concurrent features, Suspense boundaries, use() hook for data loading. |
| **TypeScript 5.5+** | End-to-end type safety with generated API client. |

**Why Next.js over Vite+React (from original plan):**
The original plan recommended Vite+React embedded in the gateway binary. Since we're
now a separate service with its own domain, Next.js provides critical advantages:
- SSR for the landing page (SEO, social sharing, fast FCP)
- Built-in API routes for OAuth callback handling
- Image optimization for NPC avatars
- Middleware for auth guards (edge runtime)
- `output: 'standalone'` produces a minimal Docker image (~50MB)

Vite remains an excellent choice if we revert to gateway-embedded. The architecture
below is framework-agnostic at the component level.

### UI & Styling

| Choice | Rationale |
|--------|-----------|
| **shadcn/ui** | Copy-paste Radix primitives. No npm lock-in. Accessible by default (ARIA). Customizable with Tailwind. |
| **Tailwind CSS 4** | Utility-first, design tokens via CSS variables, responsive out of the box. |
| **Lucide icons** | Tree-shakeable, consistent with shadcn/ui. |
| **Geist font** | Clean, modern, excellent readability at small sizes. |

### Data & State

| Choice | Rationale |
|--------|-----------|
| **TanStack Query v5** | Server-state cache, background refetch, optimistic mutations, infinite scroll. Replaces Redux for API data. |
| **Zustand** | Minimal client state (sidebar open, theme, modal stack). No boilerplate. |
| **React Hook Form + Zod** | Performant forms with schema validation. Zod schemas shared with API types. |
| **nuqs** | Type-safe URL search params for filters, pagination, search. Shareable URLs. |

### Visualization & Real-time

| Choice | Rationale |
|--------|-----------|
| **Recharts** | Lightweight, composable, responsive charts for usage/billing. Built on D3. |
| **Native WebSocket** | Live transcript streaming. TanStack Query subscription pattern for reconnect. |
| **Web Audio API** | Voice preview playback with waveform visualization. |
| **@dnd-kit** | Accessible drag-and-drop for NPC ordering, behavior rules. |

### i18n

| Choice | Rationale |
|--------|-----------|
| **next-intl** | Next.js-native i18n with App Router support. ICU message format. |
| **English MVP** | All strings extracted to `messages/en.json` from day one. German locale added in Phase 2. |

### API Client

| Choice | Rationale |
|--------|-----------|
| **openapi-typescript + openapi-fetch** | Generate types from OpenAPI 3.1 spec. Zero-runtime type safety. Works with TanStack Query. |

---

## 3. Routing Structure

```
/                                    Landing page (public, SSR)
/pricing                             Pricing page (public, SSR)
/login                               Login/Register (public)
/auth/callback/discord               OAuth2 callback handler
/auth/callback/google                OAuth2 callback handler

/dashboard                           DM dashboard (authenticated)
/campaigns                           Campaign list
/campaigns/new                       Create campaign
/campaigns/[id]                      Campaign detail/edit
/campaigns/[id]/npcs                 NPC list for campaign
/campaigns/[id]/npcs/new             Create NPC
/campaigns/[id]/npcs/[npcId]         NPC editor
/campaigns/[id]/sessions             Session history for campaign
/campaigns/[id]/lore                 Lore editor (markdown)

/sessions                            All sessions (across campaigns)
/sessions/[id]                       Session detail + transcript
/sessions/[id]/live                  Live session monitor (WebSocket)
/sessions/[id]/replay                Session replay (future)

/billing                             Billing & usage
/billing/plans                       Plan comparison & upgrade

/settings                            Account settings
/settings/api-keys                   API key management (BYOK)
/settings/notifications              Notification preferences

/admin                               Super admin dashboard
/admin/tenants                       Tenant management
/admin/tenants/[id]                  Tenant detail
/admin/system                        System health & metrics
/admin/billing                       All-tenant billing overview
/admin/users                         User management

/support                             Help & support
/support/tickets                     Ticket list
/support/tickets/new                 Create ticket
/support/docs                        Documentation links
```

### Route Groups & Layouts

```
app/
├── (public)/                        # No auth required
│   ├── layout.tsx                   # Marketing header/footer
│   ├── page.tsx                     # Landing page
│   ├── pricing/page.tsx
│   ├── login/page.tsx
│   └── auth/callback/[provider]/route.ts  # API route for OAuth
│
├── (app)/                           # Authenticated, sidebar layout
│   ├── layout.tsx                   # Sidebar + topbar + auth guard
│   ├── dashboard/page.tsx
│   ├── campaigns/
│   │   ├── page.tsx                 # List
│   │   ├── new/page.tsx
│   │   └── [id]/
│   │       ├── page.tsx             # Detail/edit
│   │       ├── npcs/
│   │       │   ├── page.tsx
│   │       │   ├── new/page.tsx
│   │       │   └── [npcId]/page.tsx
│   │       ├── sessions/page.tsx
│   │       └── lore/page.tsx
│   ├── sessions/
│   │   ├── page.tsx
│   │   └── [id]/
│   │       ├── page.tsx             # Detail + transcript
│   │       ├── live/page.tsx
│   │       └── replay/page.tsx
│   ├── billing/
│   │   ├── page.tsx
│   │   └── plans/page.tsx
│   ├── settings/
│   │   ├── page.tsx
│   │   ├── api-keys/page.tsx
│   │   └── notifications/page.tsx
│   └── support/
│       ├── page.tsx
│       ├── tickets/
│       │   ├── page.tsx
│       │   └── new/page.tsx
│       └── docs/page.tsx
│
└── (admin)/                         # super_admin only
    ├── layout.tsx                   # Admin layout with system nav
    └── admin/
        ├── page.tsx                 # Admin dashboard
        ├── tenants/
        │   ├── page.tsx
        │   └── [id]/page.tsx
        ├── system/page.tsx
        ├── billing/page.tsx
        └── users/page.tsx
```

---

## 4. Authentication Flow

### Login Options

```
┌──────────────────────────────────────────────────────────┐
│                                                          │
│              Welcome to Glyphoxa                         │
│                                                          │
│    ┌──────────────────────────────────────────────┐      │
│    │  🎮  Continue with Discord                   │      │
│    └──────────────────────────────────────────────┘      │
│    ┌──────────────────────────────────────────────┐      │
│    │  G   Continue with Google                    │      │
│    └──────────────────────────────────────────────┘      │
│                                                          │
│    ──────────────── or ────────────────                   │
│                                                          │
│    Email:    [                              ]             │
│    Password: [                              ]             │
│                                                          │
│    [         Sign in          ]                           │
│                                                          │
│    Don't have an account?  Sign up                       │
│    Forgot password?                                      │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

### OAuth2 Flow (Discord primary)

```
User clicks "Continue with Discord"
  → Next.js redirects to Discord OAuth2 authorize URL
     (scopes: identify, email, guilds)
  → User authorizes on Discord
  → Discord redirects to /auth/callback/discord?code=...
  → Next.js API route exchanges code for tokens
  → API route calls POST /api/v1/auth/discord with Discord access token
  → Backend validates, creates/finds user, returns JWT
  → Next.js sets JWT in HttpOnly cookie (Secure, SameSite=Lax)
  → Redirect to /dashboard
```

### Token Management

- **Access token:** JWT, 15min expiry, stored in HttpOnly cookie
- **Refresh token:** Opaque, 30-day expiry, stored in HttpOnly cookie
- **Silent refresh:** TanStack Query interceptor calls `/api/v1/auth/refresh`
  when a 401 is received. On failure, redirect to /login.
- **CSRF protection:** Double-submit cookie pattern. The API requires a
  `X-CSRF-Token` header matching a non-HttpOnly cookie value.

### Role-Based UI

The JWT payload contains `{ user_id, tenant_id, role }`. The frontend uses
role to conditionally render navigation items and redirect unauthorized access:

| Role | Visible Sections |
|------|-----------------|
| `super_admin` | Everything + /admin/* |
| `tenant_admin` | Dashboard, campaigns, NPCs, sessions, billing, settings, users |
| `dm` | Dashboard, campaigns (own), NPCs, sessions (own), settings |
| `viewer` | Dashboard (read-only), sessions (read-only), transcripts |

Middleware in `(app)/layout.tsx` reads the JWT cookie server-side and
redirects to /login if expired or missing. Role checks happen both
client-side (hide UI) and server-side (API returns 403).

---

## 5. Page Designs

### 5.1 Landing Page (`/`)

**Purpose:** Marketing, feature overview, pricing CTA, trust signals.

```
┌──────────────────────────────────────────────────────────────────────┐
│  [Logo] Glyphoxa          Features  Pricing  Docs     [Sign In]     │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│           Bring Your NPCs to Life                                    │
│                                                                      │
│     AI-powered voice NPCs for tabletop RPGs.                         │
│     Distinct voices. Real personalities.                             │
│     Persistent memory across sessions.                               │
│                                                                      │
│     [  Get Started Free  ]    [ Watch Demo ▶ ]                       │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                  │
│  │ 🎭 Unique   │  │ 🎙 Real     │  │ 🧠 Memory   │                  │
│  │ Personalities│  │ Voices      │  │ That Lasts  │                  │
│  │             │  │             │  │             │                  │
│  │ Each NPC has│  │ ElevenLabs, │  │ NPCs recall │                  │
│  │ their own   │  │ Azure, or   │  │ past events,│                  │
│  │ personality │  │ bring your  │  │ build        │                  │
│  │ and behavior│  │ own voice   │  │ relationships│                  │
│  └─────────────┘  └─────────────┘  └─────────────┘                  │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  How It Works                                                        │
│                                                                      │
│  1. Create your campaign and define NPCs                             │
│     ┌──────────────────────────────────────────────┐                 │
│     │  [Screenshot: NPC editor with voice config]  │                 │
│     └──────────────────────────────────────────────┘                 │
│                                                                      │
│  2. Start a session — NPCs join your voice channel                   │
│     ┌──────────────────────────────────────────────┐                 │
│     │  [Screenshot: Discord voice with NPC]        │                 │
│     └──────────────────────────────────────────────┘                 │
│                                                                      │
│  3. Players talk naturally — NPCs respond in character               │
│     ┌──────────────────────────────────────────────┐                 │
│     │  [Screenshot: Live transcript view]          │                 │
│     └──────────────────────────────────────────────┘                 │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  Pricing — see /pricing for detail                                   │
│                                                                      │
│  ┌──────────┐  ┌───────────┐  ┌───────────────┐  ┌────────────┐     │
│  │ Free     │  │ Adventurer│  │ Dungeon Master│  │ Guild      │     │
│  │ $0/mo    │  │ $9/mo     │  │ $19/mo        │  │ $29/mo     │     │
│  │ 2 sess.  │  │ 8 sess.   │  │ Unlimited     │  │ + 5 seats  │     │
│  │ 2 NPCs   │  │ 10 NPCs   │  │ Premium voice │  │ Custom     │     │
│  │          │  │           │  │               │  │ voices     │     │
│  │ [Start]  │  │ [Start]   │  │ [Start]       │  │ [Contact]  │     │
│  └──────────┘  └───────────┘  └───────────────┘  └────────────┘     │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  Trusted by DMs running campaigns in D&D 5e, Pathfinder 2e,         │
│  and more. Works with Discord and WebRTC.                            │
│                                                                      │
│  [Logo] [Logo] [Logo]  (game system logos, Discord logo)             │
│                                                                      │
├──────────────────────────────────────────────────────────────────────┤
│  © 2026 Glyphoxa   Privacy   Terms   Discord   GitHub               │
└──────────────────────────────────────────────────────────────────────┘
```

**Implementation notes:**
- Server-rendered (RSC) for SEO and fast FCP
- Hero section uses subtle CSS animation (floating NPC silhouettes)
- "Watch Demo" opens an inline video player (lazy-loaded)
- Pricing cards link to `/pricing` with anchor to selected tier
- Mobile: single-column stack, pricing cards horizontally scrollable

### 5.2 Login / Register (`/login`)

See wireframe in Section 4 above. Additional details:

- **Social auth buttons** prominent at top (Discord primary — most DMs have it)
- **Email/password** form below the separator
- **Register toggle:** "Don't have an account? Sign up" swaps the form to registration
  (name + email + password + confirm), or a separate `/register` page
- **Password requirements:** 8+ chars, shown inline as user types
- **Error states:** Inline validation, red border + error text below field
- **Loading state:** Button shows spinner, disabled during request
- **Mobile:** Full-width, large touch targets (min 44px)

### 5.3 Dashboard (`/dashboard`)

```
┌──────────────────────────────────────────────────────────────────────┐
│  ☰  Glyphoxa                                    [🔔] [Avatar ▼]    │
├────────────┬─────────────────────────────────────────────────────────┤
│            │                                                         │
│  Dashboard │   Good evening, Luk                                     │
│  ─────────│                                                         │
│  Campaigns │   ┌────────────┐ ┌────────────┐ ┌────────────┐         │
│  Sessions  │   │ Campaigns  │ │  Active    │ │  This Month │         │
│  Billing   │   │     3      │ │  Sessions  │ │  47 / 100h  │         │
│  ─────────│   │            │ │     2      │ │  ████░░░░░  │         │
│  Settings  │   └────────────┘ └────────────┘ └────────────┘         │
│  Support   │                                                         │
│            │   Active Sessions                                       │
│            │   ┌────────────────────────────────────────────┐        │
│            │   │ 🟢 Die Chroniken von Rabenheim             │        │
│            │   │   Guild: Pen & Paper DE  •  Duration: 1:42 │        │
│            │   │   NPCs: Heinrich, Elara, Erzähler          │        │
│            │   │                    [View Live] [Stop]       │        │
│            │   ├────────────────────────────────────────────┤        │
│            │   │ 🟢 Tutorial Campaign                       │        │
│            │   │   Guild: Demo Server  •  Duration: 0:05    │        │
│            │   │   NPCs: Guide                              │        │
│            │   │                    [View Live] [Stop]       │        │
│            │   └────────────────────────────────────────────┘        │
│            │                                                         │
│            │   Recent Activity                                       │
│            │   ┌────────────────────────────────────────────┐        │
│            │   │  ✓  Session ended: Rabenheim (1h 23m)      │        │
│            │   │     Today, 19:42                            │        │
│            │   │  +  NPC created: "Erzähler" in Rabenheim   │        │
│            │   │     Today, 14:15                            │        │
│            │   │  ✓  Session ended: Tutorial (0h 12m)       │        │
│            │   │     Yesterday, 21:30                        │        │
│            │   └────────────────────────────────────────────┘        │
│            │                                                         │
│            │   Quick Actions                                         │
│            │   [+ New Campaign]  [+ New NPC]  [📄 View Transcripts]  │
│            │                                                         │
└────────────┴─────────────────────────────────────────────────────────┘
```

**Key interactions:**
- Metric cards are clickable — link to respective detail pages
- Active sessions auto-refresh every 10s via TanStack Query `refetchInterval`
- "View Live" navigates to `/sessions/[id]/live`
- "Stop" shows a confirmation dialog before calling `DELETE /api/v1/sessions/{id}`
- Activity feed is a reverse-chronological list (last 20 items, "Show more" link)
- Greeting uses time-of-day logic (morning/afternoon/evening)
- **Mobile:** Metric cards in a 2x2 grid, sidebar collapses to hamburger menu

### 5.4 Campaign Management (`/campaigns`, `/campaigns/[id]`)

**Campaign List:**

```
┌─────────────────────────────────────────────────────────────────┐
│  Campaigns                                   [+ New Campaign]   │
│                                                                  │
│  ┌──────────────────────────┐  ┌──────────────────────────┐     │
│  │  🏰 Die Chroniken von    │  │  📚 Tutorial Campaign     │     │
│  │     Rabenheim             │  │                           │     │
│  │                           │  │  System: D&D 5e           │     │
│  │  System: Das Schwarze Auge│  │  NPCs: 1                  │     │
│  │  NPCs: 5                  │  │  Last session: 2 days ago │     │
│  │  Last session: 2 hours ago│  │                           │     │
│  │                           │  │  [Open]                   │     │
│  │  🟢 Active session        │  └──────────────────────────┘     │
│  │  [Open]                   │                                   │
│  └──────────────────────────┘  ┌──────────────────────────┐     │
│                                 │  + Create New Campaign    │     │
│                                 │                           │     │
│                                 │  Click to get started     │     │
│                                 └──────────────────────────┘     │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Campaign Detail/Edit (`/campaigns/[id]`):**

```
┌─────────────────────────────────────────────────────────────────┐
│  ← Campaigns  /  Die Chroniken von Rabenheim        [Save] [⋯] │
│                                                                  │
│  ┌─ Details ─┬─ NPCs ─┬─ Lore ─┬─ Sessions ─┐                  │
│  │           │        │        │             │                  │
│  ├───────────┴────────┴────────┴─────────────┘                  │
│                                                                  │
│  Campaign Name:                                                  │
│  [ Die Chroniken von Rabenheim              ]                    │
│                                                                  │
│  Game System:                                                    │
│  [ Das Schwarze Auge ▼ ]                                         │
│                                                                  │
│  Description:                                                    │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │  **B** _I_ ~~S~~ `<>` H1 H2 • ── 🔗 📷                │    │
│  ├──────────────────────────────────────────────────────────┤    │
│  │  Die Stadt Rabenheim liegt am Rand des Düsterwalds.     │    │
│  │  Seit dem Verschwinden des alten Bürgermeisters          │    │
│  │  herrscht Unruhe in der Bevölkerung...                  │    │
│  │                                                          │    │
│  │  ## Wichtige Orte                                        │    │
│  │  - Das Rathaus                                           │    │
│  │  - Der Düsterwald                                        │    │
│  │  - Die Taverne "Zum Goldenen Raben"                      │    │
│  └──────────────────────────────────────────────────────────┘    │
│                                                                  │
│  Campaign Settings (JSONB):                                      │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │  Language:     [ Deutsch ▼ ]                              │    │
│  │  Max Players:  [ 6        ]                              │    │
│  │  Auto-recap:   [✓]                                       │    │
│  └──────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ── Danger Zone ──────────────────────────────────────────────   │
│  [ Delete Campaign ]  (requires typing campaign name)            │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Tab navigation:** Details | NPCs | Lore | Sessions — each tab is a
sub-route that preserves campaign context without full page reload.

**Lore editor:** Uses a split-pane markdown editor (edit left, preview right)
built with `@uiw/react-md-editor` or a custom Tiptap setup. Supports image
upload via drag-and-drop (stored as presigned S3 URLs or inline base64 for MVP).

### 5.5 NPC Editor (`/campaigns/[id]/npcs/[npcId]`)

The most complex page. Organized into collapsible sections with a sticky
save bar at the top.

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Rabenheim / NPCs  /  Heinrich der Wächter                        │
│                                              [Discard] [Save Changes]│
│  ┌───────────────────────────────┬──────────────────────────────────┐│
│  │                               │                                  ││
│  │  ── Identity ──               │  ── Preview ──                   ││
│  │                               │                                  ││
│  │  Name:                        │  ┌────────────────────────────┐  ││
│  │  [ Heinrich der Wächter  ]    │  │  ┌──────┐                 │  ││
│  │                               │  │  │Avatar│  Heinrich der   │  ││
│  │  Avatar:                      │  │  │  🛡️  │  Wächter        │  ││
│  │  [Choose file] or drag here   │  │  └──────┘                 │  ││
│  │                               │  │                            │  ││
│  │                               │  │  "Halt! Wer geht da?"     │  ││
│  │                               │  │        [▶ Play Voice]      │  ││
│  │                               │  │                            │  ││
│  │                               │  │  Engine: Cascaded          │  ││
│  │                               │  │  Tier: Standard            │  ││
│  │                               │  │  Voice: ElevenLabs/Helmut  │  ││
│  │                               │  └────────────────────────────┘  ││
│  │                               │                                  ││
│  ├───────────────────────────────┴──────────────────────────────────┤│
│  │                                                                   ││
│  │  ── Personality ─────────────────────────────────── [▼ collapse]  ││
│  │                                                                   ││
│  │  ┌───────────────────────────────────────────────────────────┐    ││
│  │  │  Ein strenger aber gerechter Stadtwächter, der seit       │    ││
│  │  │  über 20 Jahren die Tore von Rabenheim bewacht. Er kennt │    ││
│  │  │  jeden Bewohner beim Namen und misstraut Fremden          │    ││
│  │  │  zunächst, kann aber durch Ehrlichkeit überzeugt werden.  │    ││
│  │  │                                                           │    ││
│  │  │  Heinrich hat eine tiefe, raue Stimme und spricht in     │    ││
│  │  │  kurzen, prägnanten Sätzen. Er nennt Respektspersonen    │    ││
│  │  │  beim Titel.                                              │    ││
│  │  └───────────────────────────────────────────────────────────┘    ││
│  │  Characters: 423 / 2000                                          ││
│  │                                                                   ││
│  │  ── Voice ─────────────────────────────────────────── [▼]        ││
│  │                                                                   ││
│  │  Provider:    [ ElevenLabs ▼ ]                                    ││
│  │                                                                   ││
│  │  Voice:       [ Helmut ▼ ]   [▶ Preview]                          ││
│  │                                                                   ││
│  │  Sample text: [ Halt! Wer geht da? Niemand passiert     ]        ││
│  │               [ dieses Tor ohne gültigen Passierschein.  ]        ││
│  │                                                                   ││
│  │  ┌─ Waveform ──────────────────────────────────────────┐          ││
│  │  │  ▁▂▃▅▇▅▃▂▁▂▃▅▇████▅▃▂▁▁▂▃▅▇▅▃▂▁▂▃▅▇████▅▃        │          ││
│  │  │  0:00 ─────●────────────────────────────── 0:04     │          ││
│  │  └─────────────────────────────────────────────────────┘          ││
│  │                                                                   ││
│  │  Pitch:  -10 ────────────●──── +10  (-2.0 semitones)              ││
│  │  Speed:  0.5 ──────●────────── 2.0  (1.0x)                       ││
│  │                                                                   ││
│  │  Custom Voice Sample:                                             ││
│  │  [ Upload .wav / .mp3 (max 10MB) ]                                ││
│  │  Used for voice cloning (ElevenLabs Instant Voice Clone)          ││
│  │                                                                   ││
│  │  ── Engine & Tier ────────────────────────────────── [▼]          ││
│  │                                                                   ││
│  │  Engine:                                                          ││
│  │  ┌─────────────┐ ┌─────────────┐ ┌──────────────────┐            ││
│  │  │ ● Cascaded  │ │ ○ S2S       │ │ ○ Sentence       │            ││
│  │  │   STT→LLM→  │ │   Direct    │ │   Cascade        │            ││
│  │  │   TTS       │ │   speech-to │ │   Hybrid          │            ││
│  │  │             │ │   -speech   │ │                   │            ││
│  │  │ Best quality│ │ Lowest      │ │ Good balance      │            ││
│  │  │             │ │ latency     │ │                   │            ││
│  │  └─────────────┘ └─────────────┘ └──────────────────┘            ││
│  │                                                                   ││
│  │  Budget Tier:                                                     ││
│  │  ┌──────────┐ ┌──────────────┐ ┌──────────┐                      ││
│  │  │ ○ Fast   │ │ ● Standard   │ │ ○ Deep   │                      ││
│  │  │ Quick    │ │ Balanced     │ │ Thorough │                      ││
│  │  │ responses│ │ quality &    │ │ reasoning│                      ││
│  │  │          │ │ speed        │ │ + tools  │                      ││
│  │  └──────────┘ └──────────────┘ └──────────┘                      ││
│  │                                                                   ││
│  │  ── Knowledge ───────────────────────────────────── [▼]           ││
│  │                                                                   ││
│  │  Knowledge Scope (topics this NPC knows about):                   ││
│  │  ┌──────────────────────────────────────────────────┐             ││
│  │  │ [Rabenheim history ✕] [guard duties ✕]           │             ││
│  │  │ [city layout ✕] [+ type to add...]               │             ││
│  │  └──────────────────────────────────────────────────┘             ││
│  │                                                                   ││
│  │  Secret Knowledge (facts NPC knows but won't volunteer):          ││
│  │  ┌──────────────────────────────────────────────────┐             ││
│  │  │ [The mayor's corruption ✕] [+ type to add...]    │             ││
│  │  └──────────────────────────────────────────────────┘             ││
│  │                                                                   ││
│  │  ── Behavior Rules ──────────────────────────────── [▼]           ││
│  │                                                                   ││
│  │  ≡  Spricht immer Deutsch                          [✕]           ││
│  │  ≡  Misstraut Fremden zunächst                     [✕]           ││
│  │  ≡  Nennt Respektspersonen beim Titel              [✕]           ││
│  │  [+ Add Rule]                                                     ││
│  │                                                                   ││
│  │  (≡ = drag handle for reordering via @dnd-kit)                    ││
│  │                                                                   ││
│  │  ── Advanced ─────────────────────────────────────── [▼]          ││
│  │                                                                   ││
│  │  MCP Tools:                                                       ││
│  │  ┌──────────────────────────────────────────────────┐             ││
│  │  │ [patrol_route ✕] [check_papers ✕] [+ add...]    │             ││
│  │  └──────────────────────────────────────────────────┘             ││
│  │                                                                   ││
│  │  [✓] Address Only — only responds when directly addressed         ││
│  │  [ ] GM Helper — acts as narrator/GM assistant (1 per campaign)   ││
│  │                                                                   ││
│  │  Custom Attributes (JSON):                                        ││
│  │  ┌──────────────────────────────────────────────────┐             ││
│  │  │  {                                                │             ││
│  │  │    "alignment": "lawful neutral",                 │             ││
│  │  │    "age": 52,                                     │             ││
│  │  │    "faction": "Stadtwache"                        │             ││
│  │  │  }                                                │             ││
│  │  └──────────────────────────────────────────────────┘             ││
│  │                                                                   ││
│  └──────────────────────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────────────────────────┘
```

**Key interactions:**

- **Voice preview flow:**
  1. User selects provider + voice ID + adjusts pitch/speed
  2. User types or accepts default sample text
  3. Clicks "Preview" → `POST /api/v1/npcs/{id}/voice-preview`
     with `{ text, voice_config }` body
  4. Returns audio blob (opus/mp3)
  5. Web Audio API plays audio, waveform visualizer shows amplitude
  6. Rate-limited: 5 previews/minute client-side, server enforces per-tenant

- **Custom voice upload:**
  1. User uploads .wav/.mp3 file (max 10MB)
  2. Client validates format and duration (5-30 seconds)
  3. `POST /api/v1/voices/upload` with multipart form data
  4. Backend sends to ElevenLabs Instant Voice Clone API
  5. Returns a new voice_id that can be selected in the voice dropdown
  6. Show progress bar during upload + cloning

- **Drag-and-drop behavior rules:**
  `@dnd-kit/core` + `@dnd-kit/sortable` for reordering. Keyboard accessible
  (Enter to grab, arrows to move, Enter to drop). Persists order index.

- **Tag inputs** (knowledge scope, secrets, tools):
  Combobox with free-text entry. Type to filter existing tags, Enter to add.
  Click ✕ to remove. Tags are stored as `string[]` in the NPC definition.

- **Unsaved changes warning:**
  `useBeforeUnload` hook + React Router blocker. Sticky save bar turns
  yellow when form is dirty. "Discard" resets to server state.

- **Mobile layout:**
  Single column. Preview panel moves above the form. Collapsible sections
  start collapsed except Identity and Personality. Voice controls use
  full-width sliders.

### 5.6 Session Monitoring (`/sessions`, `/sessions/[id]/live`)

**Session List:**

```
┌──────────────────────────────────────────────────────────────────┐
│  Sessions                              [Active ▼] [All time ▼]   │
│                                                                   │
│  ┌───────┬──────────────────┬──────────┬──────────┬────────────┐ │
│  │Status │ Campaign          │ Guild    │ Duration │ Actions    │ │
│  ├───────┼──────────────────┼──────────┼──────────┼────────────┤ │
│  │ 🟢    │ Rabenheim         │ PP DE    │ 1:42:15  │ [Live] [⋯]│ │
│  │ 🟢    │ Tutorial          │ Demo     │ 0:05:30  │ [Live] [⋯]│ │
│  │ ⚪    │ Rabenheim         │ PP DE    │ 1:23:00  │ [View] [⋯]│ │
│  │ ⚪    │ Tutorial          │ Demo     │ 0:12:45  │ [View] [⋯]│ │
│  │ 🔴    │ Rabenheim         │ PP DE    │ 0:03:12  │ [View] [⋯]│ │
│  └───────┴──────────────────┴──────────┴──────────┴────────────┘ │
│                                                                   │
│  Showing 5 of 47 sessions           [◄ 1  2  3  4  5 ►]         │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

Status indicators: 🟢 active, ⚪ ended, 🔴 ended with error.
Filter bar: status dropdown, campaign filter, date range picker.
Search: full-text across campaign name, guild name, error messages.

**Live Session Monitor (`/sessions/[id]/live`):**

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Sessions  /  Live: Rabenheim                      🟢 Connected   │
├─────────────────────────────────────────┬────────────────────────────┤
│                                         │                            │
│  Live Transcript                        │  Session Info              │
│                                         │                            │
│  ┌─────────────────────────────────┐    │  Campaign: Rabenheim       │
│  │                                 │    │  Guild: Pen & Paper DE     │
│  │  [19:42:15] Player (Luk):      │    │  Channel: #taverne         │
│  │  "Guten Abend, Wächter.        │    │  Duration: 1:42:15         │
│  │   Wir suchen den Bürger-       │    │  Worker: worker-2          │
│  │   meister."                     │    │                            │
│  │                                 │    │  Active NPCs               │
│  │  [19:42:18] Heinrich:          │    │  ┌────────────────────┐    │
│  │  "Halt! Der Bürgermeister      │    │  │ Heinrich  │ 3 resp │    │
│  │   empfängt keine Besucher      │    │  │ Elara     │ 1 resp │    │
│  │   nach Sonnenuntergang."       │    │  │ Erzähler  │ 5 resp │    │
│  │                                 │    │  └────────────────────┘    │
│  │  [19:42:22] Player (Sara):     │    │                            │
│  │  "Aber es ist dringend!"       │    │  Audio Stats               │
│  │                                 │    │  ┌────────────────────┐    │
│  │  ▼ auto-scroll                  │    │  │ VAD:  ▁▂▃▅▇▅▃▂▁   │    │
│  └─────────────────────────────────┘    │  │ STT:  142ms avg    │    │
│                                         │  │ LLM:  380ms avg    │    │
│                                         │  │ TTS:  210ms avg    │    │
│                                         │  │ E2E:  890ms avg    │    │
│                                         │  └────────────────────┘    │
│                                         │                            │
│                                         │  [Force Stop Session]      │
│                                         │                            │
├─────────────────────────────────────────┴────────────────────────────┤
│  Connection: WebSocket  •  Latency: 23ms  •  Messages: 47           │
└──────────────────────────────────────────────────────────────────────┘
```

**WebSocket connection:**
- Connect to `wss://api.glyphoxa.com/api/v1/sessions/{id}/live`
- JWT sent as query param or first message (WebSocket doesn't support cookies)
- Server pushes: `transcript_entry`, `audio_stats`, `session_state_change`
- Client auto-reconnects with exponential backoff (1s, 2s, 4s, max 30s)
- Connection status indicator in header (🟢 Connected / 🟡 Reconnecting / 🔴 Disconnected)
- Auto-scroll toggle: locks to bottom by default, unlocks if user scrolls up

**Mobile:** Transcript full-width, info panel in a collapsible bottom sheet.

### 5.7 Transcript Viewer (`/sessions/[id]`)

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Sessions  /  Session 2026-03-24 19:00                             │
│                                                                       │
│  ┌─ Transcript ─┬─ Details ─┬─ Stats ─┐                              │
│  │              │           │         │                              │
│  ├──────────────┴───────────┴─────────┘                              │
│                                                                       │
│  Search: [ Search transcript...        🔍 ]     [Raw ○ ● Corrected]  │
│                                                                       │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │                                                               │    │
│  │  19:00:12                                                     │    │
│  │  ┌──────────────────────────────────────────────────────┐     │    │
│  │  │  👤 Luk (Player)                                     │     │    │
│  │  │  "Wir betreten die Taverne und schauen uns um."      │     │    │
│  │  └──────────────────────────────────────────────────────┘     │    │
│  │                                                               │    │
│  │  19:00:15                                                     │    │
│  │  ┌──────────────────────────────────────────────────────┐     │    │
│  │  │  🎭 Erzähler (NPC)                                   │     │    │
│  │  │  "Die Taverne 'Zum Goldenen Raben' ist an diesem     │     │    │
│  │  │   Abend gut besucht. Am Tresen steht ein breit-      │     │    │
│  │  │   schultriger Mann und poliert Gläser."              │     │    │
│  │  │                                          1.1s ⚡     │     │    │
│  │  └──────────────────────────────────────────────────────┘     │    │
│  │                                                               │    │
│  │  19:00:22                                                     │    │
│  │  ┌──────────────────────────────────────────────────────┐     │    │
│  │  │  👤 Sara (Player)                                    │     │    │
│  │  │  "Ich gehe zum Wirt und bestelle ein Bier."          │     │    │
│  │  └──────────────────────────────────────────────────────┘     │    │
│  │                                                               │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
│  Export: [📄 Text] [📋 JSON] [📊 CSV]                                │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

**Key features:**
- **Raw vs Corrected toggle:** Shows original STT output vs LLM-corrected text
- **Search:** Client-side filter with highlighted matches (for loaded entries).
  Server-side full-text search via `GET /api/v1/sessions/{id}/transcript?q=...`
- **Infinite scroll:** Load 50 entries at a time, scroll to load more (TanStack Query `useInfiniteQuery`)
- **Response time badges:** NPC responses show end-to-end latency (⚡ < 1.2s, ⚠️ > 2s)
- **Export:** Client-side generation for text/JSON, server-side for CSV (large transcripts)
- **Color coding:** Player messages left-aligned (neutral), NPC messages right-aligned (tinted by NPC color)

### 5.8 Session Replay (Future — `/sessions/[id]/replay`)

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Session  /  Replay: Rabenheim 2026-03-24                         │
│                                                                       │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │                                                               │    │
│  │   (Transcript entries appear synchronized with audio)         │    │
│  │                                                               │    │
│  │   [Erzähler's message highlighted as audio plays]             │    │
│  │                                                               │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │  ▁▂▃▅▇▅▃▂▁▂▃▅▇████▅▃▂▁▁▂▃▅▇▅▃▂▁▂▃▅▇████▅▃▂▁▂▃▅▇████▅▃▂▁  │    │
│  │  0:00 ─────────────●──────────────────────────────── 1:42:15 │    │
│  │                                                               │    │
│  │  [⏮]  [◀◀]  [ ▶ Play ]  [▶▶]  [⏭]    1x ▼    🔊 ────●──    │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

**Design notes (future implementation):**
- Audio stored as opus/webm per-session (requires backend audio recording feature)
- Transcript entries have timestamps that sync with audio playback position
- Currently-playing entry highlighted and auto-scrolled
- Playback speed: 0.5x, 1x, 1.5x, 2x
- Skip between entries with forward/back buttons
- This page is a placeholder until audio recording is implemented

### 5.9 Billing & Usage (`/billing`)

```
┌──────────────────────────────────────────────────────────────────────┐
│  Billing & Usage                                                      │
│                                                                       │
│  Current Plan                                                         │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │  🏆  Dungeon Master Plan — $19/month                          │    │
│  │                                                               │    │
│  │  Unlimited sessions  •  Premium voices  •  Unlimited NPCs     │    │
│  │  Next billing date: April 1, 2026                             │    │
│  │                                                               │    │
│  │  [Change Plan]   [Manage Payment Method]                      │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
│  This Month's Usage  (March 2026)                                     │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐    │
│  │ Session hrs  │ │ LLM Tokens  │ │ STT Seconds │ │ TTS Chars   │    │
│  │   47.2h     │ │  1.2M       │ │  4,230s     │ │  892K       │    │
│  │   ████░░░░  │ │             │ │             │ │             │    │
│  │   47/100h   │ │ (no limit)  │ │ (no limit)  │ │ (no limit)  │    │
│  └─────────────┘ └─────────────┘ └─────────────┘ └─────────────┘    │
│                                                                       │
│  Usage Over Time                                                      │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │  Session Hours by Day                        [This month ▼]   │    │
│  │                                                               │    │
│  │  5h ┤                                                         │    │
│  │  4h ┤          ██                                             │    │
│  │  3h ┤    ██    ██         ██                                  │    │
│  │  2h ┤    ██    ██    ██   ██    ██                             │    │
│  │  1h ┤ ██ ██ ██ ██ ██ ██  ██ ██ ██                             │    │
│  │  0h ┼──┬──┬──┬──┬──┬──┬──┬──┬──┬──                           │    │
│  │       1  3  5  7  9 11 13 15 17 19 ...                        │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
│  Session Breakdown                                                    │
│  ┌──────────────────┬──────────┬──────────┬──────────┬───────────┐   │
│  │ Date             │ Duration │ Tokens   │ STT      │ TTS       │   │
│  ├──────────────────┼──────────┼──────────┼──────────┼───────────┤   │
│  │ Mar 24, 19:00    │ 1h 42m   │ 45,230   │ 156s     │ 23,400    │   │
│  │ Mar 23, 20:15    │ 2h 10m   │ 62,100   │ 210s     │ 31,200    │   │
│  │ Mar 22, 19:30    │ 1h 23m   │ 38,900   │ 134s     │ 19,800    │   │
│  └──────────────────┴──────────┴──────────┴──────────┴───────────┘   │
│                                                                       │
│  [Download CSV]                                                       │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

**"Change Plan" flow:**
Navigates to `/billing/plans` which shows the pricing comparison
(same tiers as landing page) with the current plan highlighted.
Upgrade is immediate; downgrade takes effect at end of billing period.
Payment via Stripe Checkout or Stripe Elements embedded form.

### 5.10 Settings (`/settings`)

```
┌──────────────────────────────────────────────────────────────────┐
│  Settings                                                         │
│                                                                   │
│  ┌─ Account ─┬─ API Keys ─┬─ Notifications ─┐                    │
│  │           │            │                  │                    │
│  ├───────────┴────────────┴──────────────────┘                    │
│                                                                   │
│  Profile                                                          │
│  ┌────────────────────────────────────────────────────────┐       │
│  │  Name:     [ Luk                          ]            │       │
│  │  Email:    [ luk@example.com              ]  (verified)│       │
│  │  Discord:  Connected as LukTTRPG#1234      [Unlink]    │       │
│  │  Google:   Not connected                   [Link]      │       │
│  └────────────────────────────────────────────────────────┘       │
│                                                                   │
│  Appearance                                                       │
│  ┌────────────────────────────────────────────────────────┐       │
│  │  Theme:   ( ) Light  (●) Dark  ( ) System              │       │
│  │  Language: [ English ▼ ]                                │       │
│  └────────────────────────────────────────────────────────┘       │
│                                                                   │
│  ── Danger Zone ──────────────────────────────────────────        │
│  [ Delete Account ]  (requires confirmation)                      │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

**API Keys tab (`/settings/api-keys`):**

```
┌──────────────────────────────────────────────────────────────────┐
│  API Keys — Bring Your Own                                        │
│                                                                   │
│  Use your own API keys to avoid usage limits on your plan.        │
│  Keys are encrypted and stored securely via Vault Transit.        │
│                                                                   │
│  ┌────────────────────────────────────────────────────────┐       │
│  │  OpenAI (LLM)                                          │       │
│  │  Key: sk-••••••••••••••••3kF7    [Change] [Remove]     │       │
│  │  Status: ✓ Valid (tested 2h ago)                       │       │
│  ├────────────────────────────────────────────────────────┤       │
│  │  ElevenLabs (TTS)                                      │       │
│  │  Key: Not configured              [Add Key]            │       │
│  ├────────────────────────────────────────────────────────┤       │
│  │  Deepgram (STT)                                        │       │
│  │  Key: Not configured              [Add Key]            │       │
│  └────────────────────────────────────────────────────────┘       │
│                                                                   │
│  [Test All Keys]                                                  │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

### 5.11 Admin Dashboard (`/admin` — super_admin only)

```
┌──────────────────────────────────────────────────────────────────────┐
│  Admin Dashboard                                                      │
│                                                                       │
│  System Overview                                                      │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐        │
│  │ Tenants    │ │ Active     │ │ Total      │ │ System     │        │
│  │    12      │ │ Sessions   │ │ Session hrs│ │ Health     │        │
│  │            │ │    5       │ │  342h/mo   │ │  ✓ All OK  │        │
│  └────────────┘ └────────────┘ └────────────┘ └────────────┘        │
│                                                                       │
│  Provider Health                                                      │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐  │
│  │ LLM  ✓  │ │ STT  ✓  │ │ TTS  ✓  │ │ VAD  ✓  │ │ EMB  ✓  │  │
│  │ OpenAI   │ │ Deepgram │ │ Eleven   │ │ Silero   │ │ Gemini   │  │
│  │ P50: 380 │ │ P50: 142 │ │ P50: 210 │ │ P50: 12  │ │ P50: 95  │  │
│  │ P99: 920 │ │ P99: 310 │ │ P99: 540 │ │ P99: 28  │ │ P99: 220 │  │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘  │
│                                                                       │
│  Active Sessions Across Tenants                                       │
│  ┌──────────┬───────────────┬──────────┬──────────┬────────────┐    │
│  │ Tenant   │ Campaign       │ Guild    │ Duration │ Worker     │    │
│  ├──────────┼───────────────┼──────────┼──────────┼────────────┤    │
│  │ luk      │ Rabenheim      │ PP DE    │ 1:42:15  │ worker-2   │    │
│  │ demo     │ Tutorial       │ Demo     │ 0:05:30  │ worker-1   │    │
│  │ team_a   │ Ravenloft      │ D&D Club │ 3:10:42  │ worker-3   │    │
│  └──────────┴───────────────┴──────────┴──────────┴────────────┘    │
│                                                                       │
│  Revenue This Month                                                   │
│  ┌───────────────────────────────────────────────────────────────┐    │
│  │  MRR: $156  •  New: 3  •  Churned: 0  •  Upgraded: 1         │    │
│  └───────────────────────────────────────────────────────────────┘    │
│                                                                       │
│  Quick Links                                                          │
│  [Grafana Dashboard]  [OTel Traces]  [Prometheus Metrics]             │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

### 5.12 Support (`/support`)

```
┌──────────────────────────────────────────────────────────────────┐
│  Help & Support                                                   │
│                                                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐              │
│  │ 📚 Docs     │  │ 💬 Discord  │  │ 🎫 Tickets  │              │
│  │ Browse the  │  │ Join our    │  │ Submit a    │              │
│  │ documenta-  │  │ community   │  │ support     │              │
│  │ tion        │  │ server      │  │ ticket      │              │
│  └─────────────┘  └─────────────┘  └─────────────┘              │
│                                                                   │
│  Frequently Asked Questions                                       │
│  ┌────────────────────────────────────────────────────────┐       │
│  │ ▶ How do I add my Discord bot to a server?             │       │
│  │ ▶ Why is my NPC's voice sounding different?            │       │
│  │ ▶ How do I use my own API keys?                        │       │
│  │ ▶ What game systems are supported?                     │       │
│  │ ▶ How does the knowledge graph work?                   │       │
│  │ ▶ Can players interact with NPCs in text chat?         │       │
│  └────────────────────────────────────────────────────────┘       │
│                                                                   │
│  Your Tickets                                                     │
│  ┌──────────┬──────────────────────────────┬──────────┬────────┐ │
│  │ #        │ Subject                       │ Status   │ Date   │ │
│  ├──────────┼──────────────────────────────┼──────────┼────────┤ │
│  │ 42       │ NPC not responding in voice   │ 🟡 Open  │ Mar 22 │ │
│  │ 38       │ Billing question              │ ✓ Closed │ Mar 15 │ │
│  └──────────┴──────────────────────────────┴──────────┴────────┘ │
│                                                                   │
│  [+ New Ticket]                                                   │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

---

## 6. Component Hierarchy

### Shared Layout Components

```
<RootLayout>                              # HTML shell, font loading, providers
├── <ThemeProvider>                        # Dark/light/system theme
├── <QueryClientProvider>                  # TanStack Query
├── <IntlProvider>                         # next-intl translations
│
├── (public) <MarketingLayout>            # Landing, pricing, login
│   ├── <MarketingHeader />               # Logo, nav links, sign-in button
│   ├── {children}
│   └── <MarketingFooter />               # Links, legal, social
│
├── (app) <AppLayout>                     # Authenticated pages
│   ├── <AuthGuard />                     # Redirect to /login if unauthenticated
│   ├── <Sidebar>                         # Collapsible navigation
│   │   ├── <SidebarLogo />
│   │   ├── <SidebarNav>                  # Role-filtered nav items
│   │   │   ├── <NavItem />              # Dashboard, Campaigns, Sessions, etc.
│   │   │   └── <NavGroup />             # Collapsible groups (Settings, Admin)
│   │   └── <SidebarFooter />            # User avatar, logout
│   ├── <Topbar>
│   │   ├── <Breadcrumb />               # Auto-generated from route segments
│   │   ├── <SearchCommand />            # Cmd+K global search (cmdk)
│   │   ├── <NotificationBell />         # Unread count badge
│   │   └── <UserMenu />                 # Avatar dropdown (profile, settings, logout)
│   └── <MainContent>
│       ├── <Suspense fallback={<PageSkeleton />}>
│       └── {children}
│
└── (admin) <AdminLayout>                 # Super admin pages
    ├── <AuthGuard requiredRole="super_admin" />
    ├── <AdminSidebar />                  # Admin-specific navigation
    └── {children}
```

### Page-Level Components

```
<DashboardPage>
├── <MetricCardGrid>
│   ├── <MetricCard />                    # Campaigns count
│   ├── <MetricCard />                    # Active sessions
│   └── <MetricCard variant="progress" /> # Usage quota bar
├── <ActiveSessionsList>
│   └── <ActiveSessionCard />             # Per-session row with actions
├── <RecentActivityFeed>
│   └── <ActivityItem />                  # Timestamped event entry
└── <QuickActions />                      # CTA buttons

<CampaignListPage>
├── <PageHeader title actions={<CreateButton />} />
├── <CampaignCardGrid>
│   └── <CampaignCard />                  # Name, system, NPC count, status
└── <EmptyState />                        # Shown when no campaigns exist

<CampaignDetailPage>
├── <PageHeader title breadcrumb actions={<SaveButton />} />
├── <Tabs>
│   ├── <Tab label="Details">
│   │   └── <CampaignForm />             # React Hook Form
│   ├── <Tab label="NPCs">
│   │   └── <NPCListForCampaign />
│   ├── <Tab label="Lore">
│   │   └── <MarkdownEditor />           # Split-pane edit/preview
│   └── <Tab label="Sessions">
│       └── <SessionHistoryTable />
└── <DangerZone />

<NPCEditorPage>                           # Most complex page
├── <StickyHeader>
│   ├── <Breadcrumb />
│   ├── <UnsavedIndicator />
│   ├── <DiscardButton />
│   └── <SaveButton />
├── <TwoColumnLayout>
│   ├── <LeftColumn>                      # Form (scrollable)
│   │   ├── <IdentitySection>
│   │   │   ├── <TextInput name="name" />
│   │   │   └── <AvatarUpload />
│   │   ├── <CollapsibleSection title="Personality">
│   │   │   └── <Textarea />
│   │   ├── <CollapsibleSection title="Voice">
│   │   │   ├── <VoiceProviderSelect />
│   │   │   ├── <VoiceIdSelect />
│   │   │   ├── <VoicePreviewButton />
│   │   │   ├── <AudioWaveform />
│   │   │   ├── <PitchSlider />
│   │   │   ├── <SpeedSlider />
│   │   │   └── <VoiceSampleUpload />
│   │   ├── <CollapsibleSection title="Engine & Tier">
│   │   │   ├── <EngineRadioCards />
│   │   │   └── <BudgetTierRadioCards />
│   │   ├── <CollapsibleSection title="Knowledge">
│   │   │   ├── <TagInput name="knowledgeScope" />
│   │   │   └── <TagInput name="secretKnowledge" />
│   │   ├── <CollapsibleSection title="Behavior Rules">
│   │   │   └── <SortableRulesList />    # @dnd-kit sortable
│   │   └── <CollapsibleSection title="Advanced">
│   │       ├── <TagInput name="tools" />
│   │       ├── <Checkbox name="addressOnly" />
│   │       ├── <Checkbox name="gmHelper" />
│   │       └── <JsonEditor name="attributes" />
│   └── <RightColumn>                     # Preview (sticky on desktop)
│       └── <NPCPreviewCard />
└── <UnsavedChangesDialog />

<LiveSessionPage>
├── <ConnectionStatusBar />               # WebSocket status
├── <TwoColumnLayout>
│   ├── <TranscriptStream>               # Virtualized scrolling list
│   │   ├── <TranscriptEntry variant="player" />
│   │   └── <TranscriptEntry variant="npc" />
│   └── <SessionInfoPanel>
│       ├── <SessionMetadata />
│       ├── <ActiveNPCsList />
│       ├── <AudioStatsWidget />          # Real-time latency metrics
│       └── <ForceStopButton />
└── <StatusFooter />                      # Connection, latency, message count

<BillingPage>
├── <CurrentPlanCard />
├── <UsageMetricGrid>
│   └── <UsageMetricCard />               # Session hours, tokens, etc.
├── <UsageChart />                        # Recharts line/bar chart
├── <SessionBreakdownTable />             # Paginated table
└── <ExportButton />
```

### Reusable UI Components (shadcn/ui + custom)

```
Primitives (shadcn/ui):                   Custom composites:
├── Button                                ├── MetricCard
├── Input                                 ├── TagInput (combobox + chips)
├── Textarea                              ├── CollapsibleSection
├── Select                                ├── SortableList (@dnd-kit)
├── Checkbox                              ├── AudioWaveform (Web Audio)
├── RadioGroup                            ├── VoicePreviewButton
├── Slider                                ├── MarkdownEditor
├── Dialog                                ├── JsonEditor (Monaco or simple)
├── DropdownMenu                          ├── DataTable (TanStack Table)
├── Tabs                                  ├── EmptyState
├── Badge                                 ├── StatusBadge (🟢🟡🔴)
├── Tooltip                               ├── PageHeader (title + breadcrumb + actions)
├── Skeleton                              ├── ConfirmDialog
├── Toast (sonner)                        ├── FileUpload (drag & drop zone)
├── Command (cmdk)                        └── InfiniteScrollList
├── Sheet (mobile sidebar)
├── Card
├── Avatar
└── Separator
```

---

## 7. Key Interactions

### 7.1 Voice Preview

```
User action                        System behavior
───────────                        ─────────────────
Select voice provider       →      Load available voices for provider
Select voice ID             →      Enable preview button
Click "Preview"             →      1. Disable button, show spinner
                                   2. POST /api/v1/npcs/{id}/voice-preview
                                      body: { text, voice: { provider, voice_id, pitch, speed } }
                                   3. Receive audio blob (opus/mp3)
                                   4. Decode with Web Audio API (AudioContext)
                                   5. Display waveform (AnalyserNode → canvas)
                                   6. Auto-play audio
                                   7. Re-enable button
Adjust pitch/speed slider   →      Debounce 300ms, then auto-preview
Click waveform              →      Seek to position in audio
Rate limit exceeded         →      Toast: "Too many previews. Wait 30s."
                                   Button disabled with countdown
```

**Web Audio API setup:**
```typescript
// Simplified — actual implementation in src/hooks/useVoicePreview.ts
const audioContext = new AudioContext();
const analyser = audioContext.createAnalyser();

async function playPreview(audioBlob: Blob) {
  const buffer = await audioContext.decodeAudioData(await audioBlob.arrayBuffer());
  const source = audioContext.createBufferSource();
  source.buffer = buffer;
  source.connect(analyser);
  analyser.connect(audioContext.destination);
  source.start();
  drawWaveform(analyser); // requestAnimationFrame loop
}
```

### 7.2 Drag-and-Drop NPC Ordering

Within a campaign's NPC list, NPCs can be reordered via drag-and-drop to
control their priority (first NPC is the default responder).

```
Drag start (mouse/touch)    →      1. Show drag overlay with NPC card
                                   2. Other items animate to make space
                                   3. Keyboard: Enter to pick up, arrows to move
Drop                        →      1. Animate into new position
                                   2. Optimistic update (TanStack Query)
                                   3. PATCH /api/v1/campaigns/{id}/npcs/order
                                      body: { npc_ids: ["id1", "id3", "id2"] }
                                   4. On error: revert optimistic update + toast
```

### 7.3 Live Transcript WebSocket

```
Component mount             →      1. Connect to wss://api/v1/sessions/{id}/live
                                   2. Send auth: { type: "auth", token: jwt }
                                   3. Server sends session snapshot (last 50 entries)
                                   4. Render initial transcript

Server push                 →      Message types:
                                   - transcript_entry: { speaker, text, npc_id, ts }
                                   - audio_stats: { vad_active, stt_ms, llm_ms, tts_ms, e2e_ms }
                                   - session_state: { state: "ended", error? }
                                   - heartbeat: { ts } (every 30s)

Client receives entry       →      1. Append to virtualized list
                                   2. Auto-scroll if at bottom
                                   3. If scrolled up: show "N new messages ↓" pill

Connection lost             →      1. Show 🟡 Reconnecting
                                   2. Exponential backoff: 1s, 2s, 4s, ..., 30s max
                                   3. On reconnect: request entries since last ts
                                   4. Show 🟢 Connected

Session ends                →      1. Show "Session ended" banner
                                   2. Close WebSocket
                                   3. Show link to transcript viewer
```

### 7.4 Global Search (Cmd+K)

Powered by `cmdk` (shadcn/ui Command component):

```
Cmd+K                       →      Open command palette
Type query                  →      Search across:
                                   - Campaigns (by name)
                                   - NPCs (by name, campaign)
                                   - Sessions (by campaign, date)
                                   - Pages (Dashboard, Settings, etc.)
                                   Debounced 200ms, client-side for pages,
                                   server-side for entities
Select result               →      Navigate to entity page
Escape                      →      Close palette
```

---

## 8. Accessibility

### WCAG 2.1 AA Compliance

- **Color contrast:** All text meets 4.5:1 ratio (Tailwind defaults + verified).
  Status indicators use shapes + text alongside color (not color alone).
- **Keyboard navigation:** All interactive elements focusable with Tab.
  Custom components (sliders, drag-and-drop, tag inputs) have keyboard handlers.
- **Screen readers:** shadcn/ui components use Radix ARIA patterns.
  Custom components have `aria-label`, `aria-describedby`, `role` attributes.
- **Focus management:** Dialog opens → focus trapped. Dialog closes → focus returns.
  Page navigation → focus moves to main heading (via `useEffect` + `ref.focus()`).
- **Motion:** `prefers-reduced-motion` respected. Drag-and-drop falls back to
  button-based reorder. Waveform animation paused.
- **Forms:** Every input has a visible label. Error messages linked via `aria-describedby`.
  Required fields marked with `aria-required`. Form-level error summary at top.
- **Skip links:** "Skip to main content" link visible on focus.
- **Language:** `<html lang="en">` set, updated for i18n. NPC content that's in
  a different language can be wrapped in `<span lang="de">`.

### Touch Targets

- Minimum 44x44px for all interactive elements (buttons, links, inputs)
- Sidebar nav items: 48px height
- Mobile: 12px minimum gap between adjacent targets

---

## 9. Performance Strategy

### Code Splitting & Lazy Loading

```
Route-based splitting (automatic with Next.js App Router):
/                  → ~50KB (landing, SSR, minimal JS)
/dashboard         → ~80KB (charts, metric cards)
/campaigns/[id]/npcs/[npcId]  → ~120KB (heaviest: editor, audio, dnd-kit)
/sessions/[id]/live → ~60KB (WebSocket, virtualized list)
/billing           → ~70KB (Recharts)

Component-level lazy loading:
- MarkdownEditor     → dynamic(() => import('./MarkdownEditor'))
- JsonEditor         → dynamic(() => import('./JsonEditor'))
- AudioWaveform      → dynamic(() => import('./AudioWaveform'))
- UsageChart         → dynamic(() => import('./UsageChart'))
- KnowledgeGraph     → dynamic(() => import('./KnowledgeGraph'))  // Phase 3
```

### Data Loading

| Pattern | Used for |
|---------|----------|
| **Server Components** | Landing page, initial page shells, SEO content |
| **TanStack Query** | All API data. `staleTime: 30s` for lists, `60s` for detail. |
| **Optimistic updates** | NPC save, campaign edit, rule reorder, session stop |
| **Infinite scroll** | Transcript entries (50/page), session history, activity feed |
| **Polling** | Active sessions (10s), dashboard metrics (30s) |
| **WebSocket** | Live transcript only (dedicated connection per active session) |
| **Prefetch** | Hover on campaign card → prefetch campaign detail + NPCs |

### Bundle Optimization

- **Tree shaking:** ES modules only. No barrel exports in component library.
- **Font subsetting:** Geist loaded via `next/font` with Latin subset.
- **Image optimization:** `next/image` with WebP/AVIF, lazy loading, blur placeholders.
- **CSS:** Tailwind purge removes unused classes. Final CSS ~15-25KB gzipped.
- **Icons:** Lucide tree-shakes to only imported icons.

### Target Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| **FCP** (landing) | < 1.0s | Lighthouse, real user monitoring |
| **LCP** (landing) | < 2.0s | Lighthouse |
| **TTI** (dashboard) | < 2.5s | Lighthouse |
| **CLS** | < 0.1 | Lighthouse |
| **JS bundle** (initial) | < 100KB gzipped | Build analysis |
| **API response** (p95) | < 200ms | Server-side metrics |

---

## 10. Responsive Design

### Breakpoints (Tailwind defaults)

| Breakpoint | Width | Layout |
|-----------|-------|--------|
| `sm` | 640px | Single column, hamburger nav |
| `md` | 768px | Two columns where needed |
| `lg` | 1024px | Full sidebar + content |
| `xl` | 1280px | Wider content area |
| `2xl` | 1536px | Max-width container |

### Mobile Adaptations

| Desktop | Mobile |
|---------|--------|
| Sidebar always visible | Sheet overlay (hamburger toggle) |
| Two-column NPC editor | Single column, preview at top |
| Data tables | Card-based list or horizontal scroll |
| Inline form sections | Collapsed accordions (expand on tap) |
| Hover tooltips | Long-press or info icons |
| Cmd+K search | Search icon in topbar |
| Side-by-side markdown editor | Tabs: Edit / Preview |
| Live monitor split view | Transcript full width, info in bottom sheet |

### PWA Considerations (Future)

The app is structured to support Progressive Web App features later:
- `manifest.json` for home screen installation
- Service worker for offline NPC editing (drafts saved to IndexedDB)
- Push notifications for session events

---

## 11. Error Handling & Loading States

### Loading Patterns

```
Initial page load     →  Full-page skeleton (shadcn Skeleton components)
Data refetch          →  Subtle shimmer on stale data (no layout shift)
Mutation in progress  →  Button spinner + disabled state
Long operation        →  Progress bar or step indicator
WebSocket reconnect   →  Banner: "Reconnecting..." with spinner
```

### Error Patterns

```
API 400 (validation)  →  Inline field errors (red border + message below)
API 401 (auth)        →  Silent refresh attempt → if fails, redirect to /login
API 403 (forbidden)   →  Toast: "You don't have permission for this action"
API 404 (not found)   →  Full-page "Not Found" with link to parent
API 429 (rate limit)  →  Toast: "Too many requests. Please wait."
API 500 (server)      →  Toast: "Something went wrong. Please try again."
                         + Retry button for idempotent operations
Network error         →  Banner: "Connection lost. Retrying..."
                         + TanStack Query auto-retry (3 attempts)
Form submission fail  →  Error summary at top of form + scroll to first error
WebSocket disconnect  →  Connection status indicator + auto-reconnect
```

### Empty States

Every list view has a designed empty state:
- **No campaigns:** Illustration + "Create your first campaign" CTA
- **No NPCs:** "Add an NPC to bring your campaign to life" + example templates
- **No sessions:** "Start a session in Discord to see it here"
- **No transcripts:** "Run a session to generate transcripts"

---

## 12. i18n Strategy

### Phase 1 (English MVP)

- All user-facing strings extracted to `messages/en.json` using `next-intl`
- ICU message format for plurals, dates, numbers:
  ```json
  {
    "dashboard.activeSessions": "{count, plural, =0 {No active sessions} one {# active session} other {# active sessions}}",
    "billing.usage": "Used {used} of {total} hours"
  }
  ```
- Date/time formatting via `Intl.DateTimeFormat` (locale-aware)
- Number formatting via `Intl.NumberFormat` (locale-aware)
- No hardcoded strings in components

### Phase 2 (German)

- Add `messages/de.json`
- URL prefix routing: `/de/dashboard`, `/en/dashboard`
- Language picker in settings + browser `Accept-Language` detection
- NPC content (personalities, rules) is user-authored and not translated —
  the UI chrome is translated, content stays in whatever language the DM writes

### RTL / Other Languages (Future)

- Tailwind's logical properties (`ms-4` instead of `ml-4`) from day one
- `dir` attribute responsive to locale

---

## 13. Directory Structure

```
web/
├── public/
│   ├── favicon.ico
│   ├── og-image.png                      # Social sharing image
│   └── fonts/                            # Geist font files
│
├── src/
│   ├── app/                              # Next.js App Router (see Section 3)
│   │   ├── layout.tsx                    # Root layout
│   │   ├── (public)/
│   │   ├── (app)/
│   │   └── (admin)/
│   │
│   ├── components/
│   │   ├── ui/                           # shadcn/ui primitives (generated)
│   │   │   ├── button.tsx
│   │   │   ├── input.tsx
│   │   │   └── ...
│   │   ├── layout/                       # Layout shells
│   │   │   ├── sidebar.tsx
│   │   │   ├── topbar.tsx
│   │   │   ├── breadcrumb.tsx
│   │   │   └── page-header.tsx
│   │   ├── campaign/                     # Campaign-specific composites
│   │   │   ├── campaign-card.tsx
│   │   │   ├── campaign-form.tsx
│   │   │   └── campaign-settings.tsx
│   │   ├── npc/                          # NPC-specific composites
│   │   │   ├── npc-card.tsx
│   │   │   ├── npc-form.tsx
│   │   │   ├── voice-preview.tsx
│   │   │   ├── voice-sample-upload.tsx
│   │   │   ├── engine-selector.tsx
│   │   │   ├── knowledge-tags.tsx
│   │   │   └── behavior-rules.tsx
│   │   ├── session/                      # Session-specific composites
│   │   │   ├── session-table.tsx
│   │   │   ├── live-transcript.tsx
│   │   │   ├── transcript-entry.tsx
│   │   │   ├── audio-stats.tsx
│   │   │   └── connection-status.tsx
│   │   ├── billing/                      # Billing composites
│   │   │   ├── plan-card.tsx
│   │   │   ├── usage-chart.tsx
│   │   │   └── usage-metric.tsx
│   │   └── shared/                       # Cross-cutting composites
│   │       ├── metric-card.tsx
│   │       ├── tag-input.tsx
│   │       ├── collapsible-section.tsx
│   │       ├── sortable-list.tsx
│   │       ├── audio-waveform.tsx
│   │       ├── markdown-editor.tsx
│   │       ├── json-editor.tsx
│   │       ├── data-table.tsx
│   │       ├── empty-state.tsx
│   │       ├── confirm-dialog.tsx
│   │       ├── file-upload.tsx
│   │       └── infinite-scroll.tsx
│   │
│   ├── hooks/                            # Custom React hooks
│   │   ├── use-voice-preview.ts          # Web Audio playback + waveform
│   │   ├── use-websocket.ts              # WebSocket with reconnect
│   │   ├── use-auth.ts                   # JWT token management
│   │   ├── use-role.ts                   # Role-based access checks
│   │   ├── use-unsaved-changes.ts        # Form dirty tracking
│   │   └── use-debounce.ts
│   │
│   ├── api/                              # API client layer
│   │   ├── client.ts                     # openapi-fetch configured instance
│   │   ├── types.ts                      # Generated from OpenAPI spec
│   │   ├── queries/                      # TanStack Query hooks (per domain)
│   │   │   ├── campaigns.ts
│   │   │   ├── npcs.ts
│   │   │   ├── sessions.ts
│   │   │   ├── usage.ts
│   │   │   ├── users.ts
│   │   │   └── auth.ts
│   │   └── mutations/                    # TanStack mutation hooks
│   │       ├── campaigns.ts
│   │       ├── npcs.ts
│   │       ├── sessions.ts
│   │       └── auth.ts
│   │
│   ├── lib/                              # Utilities
│   │   ├── auth.ts                       # Token parsing, role checks
│   │   ├── format.ts                     # Date, number, duration formatting
│   │   ├── cn.ts                         # Tailwind class merge utility
│   │   └── constants.ts                  # Route paths, config values
│   │
│   ├── messages/                         # i18n translation files
│   │   ├── en.json
│   │   └── de.json                       # Phase 2
│   │
│   └── styles/
│       └── globals.css                   # Tailwind base + custom tokens
│
├── next.config.ts
├── tailwind.config.ts
├── tsconfig.json
├── package.json
├── Dockerfile
└── .env.example                          # NEXT_PUBLIC_API_URL, etc.
```

---

## 14. Deployment

### Docker Image

```dockerfile
FROM node:22-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public

EXPOSE 3000
CMD ["node", "server.js"]
```

**Image size:** ~50MB (standalone output, Alpine base)

### Environment Variables

```env
NEXT_PUBLIC_API_URL=https://api.glyphoxa.com     # Gateway API base URL
NEXT_PUBLIC_WS_URL=wss://api.glyphoxa.com        # WebSocket base URL
NEXT_PUBLIC_DISCORD_CLIENT_ID=...                 # For OAuth redirect
NEXT_PUBLIC_GOOGLE_CLIENT_ID=...                  # For OAuth redirect
```

### K3s Deployment

```yaml
# Abbreviated — full manifest in infra/
apiVersion: apps/v1
kind: Deployment
metadata:
  name: glyphoxa-web
spec:
  replicas: 2                    # Stateless, horizontally scalable
  template:
    spec:
      containers:
      - name: web
        image: ghcr.io/glyphoxa/web:latest
        ports:
        - containerPort: 3000
        resources:
          requests: { cpu: 50m, memory: 64Mi }
          limits: { cpu: 200m, memory: 256Mi }
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: glyphoxa-web
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  rules:
  - host: app.glyphoxa.com
    http:
      paths:
      - path: /
        backend:
          service:
            name: glyphoxa-web
            port: { number: 3000 }
  tls:
  - hosts: [app.glyphoxa.com]
    secretName: glyphoxa-web-tls
```

### CORS Configuration (Gateway)

Since the frontend is on a separate domain, the Gateway API needs CORS headers:

```
Access-Control-Allow-Origin: https://app.glyphoxa.com
Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type, X-CSRF-Token
Access-Control-Allow-Credentials: true
Access-Control-Max-Age: 86400
```

---

## 15. Phase Breakdown (Frontend)

### Phase 1: MVP

**Scope:** Core CRUD pages, API key auth, basic monitoring.

- [ ] Next.js scaffolding (App Router, Tailwind, shadcn/ui, TanStack Query)
- [ ] API client generation from OpenAPI spec
- [ ] Marketing layout (landing page, pricing)
- [ ] API key login page
- [ ] App layout (sidebar, topbar, breadcrumbs)
- [ ] Dashboard (metrics, active sessions, activity feed)
- [ ] Campaign list + create/edit
- [ ] NPC list + full editor (all sections)
- [ ] Voice preview with Web Audio API
- [ ] Session list + transcript viewer
- [ ] Basic usage display
- [ ] Settings page (profile, appearance)
- [ ] Support page (FAQ, docs links)
- [ ] Responsive design pass (mobile)
- [ ] Docker image + K3s deployment
- [ ] i18n setup (English strings extracted)

**Estimated:** ~50-60 components, ~30 route pages

### Phase 2: Auth + Live + i18n

- [ ] Discord OAuth2 login flow
- [ ] Google OAuth2 login flow
- [ ] JWT-based auth with silent refresh
- [ ] Role-based navigation and access guards
- [ ] User management page (admin)
- [ ] Live session monitoring (WebSocket)
- [ ] Audio stats real-time visualization
- [ ] German locale (`messages/de.json`)
- [ ] Notification preferences
- [ ] Billing integration (Stripe)
- [ ] Plan upgrade/downgrade flow
- [ ] Voice sample upload for custom voices

### Phase 3: Advanced

- [ ] Session replay with synced audio
- [ ] Knowledge graph visualization
- [ ] Admin dashboard (all tenants, system health)
- [ ] Audit log viewer
- [ ] Usage CSV export
- [ ] PWA support (offline NPC editing)
- [ ] A/B testing infrastructure

---

## 16. Open Questions

1. **Separate domain vs subdomain:** `app.glyphoxa.com` (subdomain) vs
   `glyphoxa.app` (separate domain)? Subdomain is simpler for shared cookies
   with `api.glyphoxa.com` if both are under `*.glyphoxa.com`.

2. **SSR vs static export:** Should the dashboard pages be SSR (for
   server-side auth checks) or static with client-side auth? Recommendation:
   SSR for auth pages, static for marketing pages.

3. **Stripe integration timeline:** Should billing/payments be in Phase 1
   (if we want to charge from launch) or Phase 2 (launch free, add payments later)?

4. **NPC templates:** Should we ship pre-built NPC templates (tavern keeper,
   guard, merchant) that DMs can clone and customize? This would significantly
   improve onboarding for new users.

5. **Collaborative editing:** Multiple DMs editing the same campaign
   simultaneously? This adds significant complexity (CRDTs or OT). Defer
   to Phase 3+ unless there's strong demand.

6. **Analytics:** Do we need user analytics (page views, feature usage) for
   product decisions? If yes, recommend PostHog (self-hosted, privacy-friendly)
   or Plausible.
