# 05 — Billing & Pricing System Design

**Status:** Draft
**Date:** 2026-03-24
**References:** [pricing-models-assessment.md](./pricing-models-assessment.md)

---

## 1. Overview

Glyphoxa operates in two deployment modes with distinct billing implications:

- **SaaS (Managed):** Glyphoxa hosts everything. DMs pay a subscription. Billing system manages subscriptions, enforces limits, and processes payments via Stripe.
- **Self-Hosted:** User runs Glyphoxa with their own API keys. No subscription required for core features. Optional license key unlocks managed-service features (priority support, hosted knowledge graph, voice cloning).

The billing system gates session creation based on the DM's subscription tier. It wraps the existing `QuotaGuard` → `Orchestrator` chain — no changes to the voice pipeline itself.

### Design Principles

1. **DM pays, players benefit.** Only the DM (session owner) needs a subscription. Players join for free.
2. **Never charge per-message.** Session caps are the only consumption limit. Once a session starts, it runs without metering anxiety.
3. **Hard caps, not soft.** When you hit your session limit, the next `/session start` is rejected with a clear upgrade prompt. No surprise bills, no overages.
4. **Sessions are sacred.** A running session is never interrupted for billing reasons. Caps are checked at session start only.
5. **Self-hosted is genuinely free.** Bring-your-own-keys users get the full voice pipeline. Billing only applies to the managed service.

---

## 2. Subscription Tiers

### Tier Matrix

| | **Apprentice** (Free) | **Adventurer** ($9/mo) | **Dungeon Master** ($19/mo) | **Guild** ($29/mo) |
|---|---|---|---|---|
| **Sessions/month** | 2 | 8 | Unlimited | Unlimited |
| **Max session length** | 2 hours | 4 hours | 8 hours | 8 hours |
| **NPCs per campaign** | 2 | 10 | Unlimited | Unlimited |
| **LLM** | Gemini Flash | GPT-4o-mini | GPT-4o | GPT-4o |
| **Voice quality** | Basic (gTTS/Piper) | Standard (ElevenLabs) | Premium (ElevenLabs HD) | Premium + custom cloning |
| **Knowledge graph** | No | No | Yes | Yes |
| **Player seats** | — | — | — | 5 (shared management) |
| **Priority support** | — | — | — | Yes |
| **Annual price** | — | $90/yr (2 mo free) | $190/yr (2 mo free) | $290/yr (2 mo free) |

### Cost/Margin Analysis

| Tier | Infra cost/session (4h avg) | Sessions/mo | Monthly cost | Price | Margin |
|---|---|---|---|---|---|
| Apprentice | ~$0.80 (Flash) | 2 | ~$1.60 | $0 | -$1.60 (acquisition) |
| Adventurer | ~$2.00 (4o-mini) | 8 | ~$16.00 | $9 | -$7.00 (subsidized) |
| Dungeon Master | ~$6.40 (4o) | ~8 avg | ~$51.20 | $19 | -$32.20 (subsidized) |
| Guild | ~$6.40 (4o) | ~8 avg | ~$51.20 | $29 | -$22.20 (subsidized) |

> **Note:** These margins are negative at current API prices. This is expected for an early-stage product focused on adoption. Mitigation strategies:
> - Negotiate volume pricing with providers as usage grows
> - Session length caps limit worst-case cost per session
> - "Unlimited" tiers will have soft abuse detection (e.g. >50 sessions/month triggers review)
> - As self-hosted users bring their own keys, the managed service only bears cost for users who want convenience

### What Counts as a Session?

A **session** is counted when:
1. A DM invokes `/session start` and the gateway creates a session record in `sessions` table
2. The session transitions to `SessionActive` (worker confirms pipeline is running)

A session is **not** counted if:
- It fails to start (stays in `SessionPending` and is cleaned up)
- It ends within 60 seconds (grace period — accidental starts)

**Session length caps** are enforced by the gateway via a timer. When the cap is reached, the DM gets a 5-minute warning, then the session ends gracefully (final NPC goodbyes, transcript saved).

### NPC Count Enforcement

NPC count is checked at two points:
1. **NPC creation** — Web management API rejects creation if campaign NPC count >= tier limit
2. **Session start** — Gateway validates NPC count in `StartSessionRequest.NPCConfigs` against tier limit

The NPC store (`npcstore`) already returns NPCs per campaign. The billing layer adds a count check.

---

## 3. Data Model

### New Tables

```sql
-- Subscription plans (seeded, not user-editable)
CREATE TABLE subscription_plans (
    id              TEXT PRIMARY KEY,          -- 'apprentice', 'adventurer', 'dungeon_master', 'guild'
    name            TEXT NOT NULL,
    price_monthly   INTEGER NOT NULL,          -- cents (e.g. 900 = $9.00)
    price_yearly    INTEGER NOT NULL,          -- cents
    stripe_price_id_monthly TEXT,              -- Stripe Price ID for monthly
    stripe_price_id_yearly  TEXT,              -- Stripe Price ID for annual
    session_cap     INTEGER NOT NULL,          -- 0 = unlimited
    max_session_hours NUMERIC(4,1) NOT NULL,   -- per-session length cap
    max_npcs        INTEGER NOT NULL,          -- 0 = unlimited
    llm_tier        TEXT NOT NULL,             -- 'budget', 'standard', 'premium'
    voice_tier      TEXT NOT NULL,             -- 'basic', 'standard', 'premium'
    knowledge_graph BOOLEAN NOT NULL DEFAULT FALSE,
    player_seats    INTEGER NOT NULL DEFAULT 0,
    priority_support BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ DEFAULT now()
);

-- One subscription per tenant (1:1 with tenants table)
CREATE TABLE subscriptions (
    id                  TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    tenant_id           TEXT NOT NULL UNIQUE REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id             TEXT NOT NULL REFERENCES subscription_plans(id),
    stripe_customer_id  TEXT,                  -- Stripe Customer ID
    stripe_subscription_id TEXT,               -- Stripe Subscription ID
    billing_interval    TEXT NOT NULL DEFAULT 'monthly', -- 'monthly' or 'yearly'
    status              TEXT NOT NULL DEFAULT 'active',
        -- active: in good standing
        -- trialing: trial period (no card required)
        -- past_due: payment failed, in grace period
        -- suspended: grace period expired, sessions blocked
        -- cancelled: user cancelled, active until period end
    current_period_start TIMESTAMPTZ NOT NULL,
    current_period_end   TIMESTAMPTZ NOT NULL,
    trial_end            TIMESTAMPTZ,          -- null if no trial
    cancel_at_period_end BOOLEAN NOT NULL DEFAULT FALSE,
    grace_period_end     TIMESTAMPTZ,          -- set when payment fails
    created_at           TIMESTAMPTZ DEFAULT now(),
    updated_at           TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_subscriptions_stripe_customer ON subscriptions(stripe_customer_id);
CREATE INDEX idx_subscriptions_status ON subscriptions(status);

-- Session billing events (extends existing usage_records with granular tracking)
CREATE TABLE billing_events (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    event_type      TEXT NOT NULL,             -- 'session_start', 'session_end'
    session_minutes NUMERIC(10,2),             -- actual duration
    plan_id         TEXT NOT NULL,             -- plan at time of session
    period          DATE NOT NULL,             -- billing period (1st of month)
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_billing_events_tenant_period ON billing_events(tenant_id, period);

-- Payment history (synced from Stripe webhooks)
CREATE TABLE payment_history (
    id                  BIGSERIAL PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    stripe_invoice_id   TEXT UNIQUE,
    stripe_charge_id    TEXT,
    amount              INTEGER NOT NULL,       -- cents
    currency            TEXT NOT NULL DEFAULT 'usd',
    status              TEXT NOT NULL,          -- 'succeeded', 'failed', 'refunded'
    period_start        TIMESTAMPTZ,
    period_end          TIMESTAMPTZ,
    failure_reason      TEXT,
    created_at          TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_payment_history_tenant ON payment_history(tenant_id);
```

### Tenant Model Extension

The existing `tenants` table gains no new columns. Instead, billing state lives in the `subscriptions` table, joined by `tenant_id`. The existing `monthly_session_hours` field on `tenants` becomes a fallback for self-hosted deployments without a subscription record.

```go
// Subscription represents a tenant's billing state.
type Subscription struct {
    ID                   string
    TenantID             string
    PlanID               string
    StripeCustomerID     string
    StripeSubscriptionID string
    BillingInterval      string    // "monthly" or "yearly"
    Status               SubscriptionStatus
    CurrentPeriodStart   time.Time
    CurrentPeriodEnd     time.Time
    TrialEnd             *time.Time
    CancelAtPeriodEnd    bool
    GracePeriodEnd       *time.Time
    CreatedAt            time.Time
    UpdatedAt            time.Time
}

type SubscriptionStatus string

const (
    StatusActive    SubscriptionStatus = "active"
    StatusTrialing  SubscriptionStatus = "trialing"
    StatusPastDue   SubscriptionStatus = "past_due"
    StatusSuspended SubscriptionStatus = "suspended"
    StatusCancelled SubscriptionStatus = "cancelled"
)
```

---

## 4. Architecture

### Component Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                     Web Management UI                        │
│  (React — subscription management, usage dashboard, etc.)    │
└──────────────────────┬──────────────────────────────────────┘
                       │ REST API
                       ▼
┌─────────────────────────────────────────────────────────────┐
│                    Billing API Service                        │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │ Subscription  │  │   Usage      │  │  Stripe Webhook  │   │
│  │  Management   │  │  Dashboard   │  │    Handler       │   │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘   │
│         │                 │                    │              │
│         ▼                 ▼                    ▼              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │              Billing Store (PostgreSQL)              │     │
│  │  subscription_plans | subscriptions | billing_events │     │
│  │  payment_history                                     │     │
│  └─────────────────────────────────────────────────────┘     │
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
┌─────────────┐  ┌──────────────┐  ┌───────────┐
│   Stripe    │  │   Gateway    │  │  Admin    │
│   (extern)  │  │ (session     │  │   API     │
│             │  │  auth gate)  │  │           │
└─────────────┘  └──────────────┘  └───────────┘
```

### Session Start Authorization Flow

```
DM: /session start
       │
       ▼
┌─────────────────────────────┐
│ GatewaySessionController    │
│   .Start()                  │
└──────────┬──────────────────┘
           │
           ▼
┌─────────────────────────────┐     ┌───────────────────┐
│ BillingAuthorizer           │────▶│ SubscriptionCache │
│   .ValidateAndCreate()      │     │ (in-memory, TTL)  │
│                             │     └───────────────────┘
│ 1. Load subscription        │
│ 2. Check status:            │
│    - suspended → REJECT     │
│    - cancelled → check      │
│      period_end             │
│    - past_due → ALLOW       │
│      (grace period)         │
│    - active → ALLOW         │
│ 3. Check plan limits:       │
│    - session count this     │
│      period < cap?          │
│    - NPC count <= max?      │
│ 4. Validate model/voice     │
│    tier matches plan        │
└──────────┬──────────────────┘
           │ (if allowed)
           ▼
┌─────────────────────────────┐
│ QuotaGuard                  │
│   .ValidateAndCreate()      │
│                             │
│ (existing — checks          │
│  monthly_session_hours      │
│  from usage_records)        │
└──────────┬──────────────────┘
           │
           ▼
┌─────────────────────────────┐
│ Orchestrator                │
│   .ValidateAndCreate()      │
│                             │
│ (existing — checks license  │
│  constraints, concurrent    │
│  session limits)            │
└──────────┬──────────────────┘
           │
           ▼
    Session Created
```

### How the Gateway Knows the DM's Subscription

The gateway resolves subscription status through this chain:

1. **Discord command arrives** → extract `guildID` from interaction event
2. **Tenant lookup** → `SELECT * FROM tenants WHERE $1 = ANY(guild_ids)` (existing)
3. **Subscription lookup** → `SELECT * FROM subscriptions WHERE tenant_id = $1` (new)
4. **Plan lookup** → `SELECT * FROM subscription_plans WHERE id = $1` (new, cached)

**Caching strategy:**
- `subscription_plans` — cached indefinitely (static seed data, invalidate on deploy)
- `subscriptions` — cached per-tenant with 5-minute TTL
- Cache invalidated immediately on Stripe webhook events
- Cache key: `billing:sub:{tenant_id}`

```go
// SubscriptionCache wraps subscription lookups with in-memory TTL cache.
type SubscriptionCache struct {
    store    BillingStore
    cache    sync.Map          // tenant_id → cachedEntry
    ttl      time.Duration     // 5 minutes
}

type cachedEntry struct {
    sub       *Subscription
    plan      *SubscriptionPlan
    fetchedAt time.Time
}

func (c *SubscriptionCache) Get(ctx context.Context, tenantID string) (*Subscription, *SubscriptionPlan, error) {
    if entry, ok := c.cache.Load(tenantID); ok {
        e := entry.(*cachedEntry)
        if time.Since(e.fetchedAt) < c.ttl {
            return e.sub, e.plan, nil
        }
    }
    // Cache miss or expired — hit DB
    sub, err := c.store.GetSubscription(ctx, tenantID)
    // ...
    plan, err := c.store.GetPlan(ctx, sub.PlanID)
    // ...
    c.cache.Store(tenantID, &cachedEntry{sub: sub, plan: plan, fetchedAt: time.Now()})
    return sub, plan, nil
}

// Invalidate is called from Stripe webhook handler.
func (c *SubscriptionCache) Invalidate(tenantID string) {
    c.cache.Delete(tenantID)
}
```

---

## 5. Stripe Integration

### Why Stripe

- Industry standard for SaaS subscription billing
- Native support for subscription lifecycle (create, upgrade, downgrade, cancel, retry)
- Webhook-driven — no polling required
- Stripe Checkout for PCI-compliant payment collection (no card data touches our servers)
- Stripe Customer Portal for self-service billing management
- Go SDK: `github.com/stripe/stripe-go/v82`

### Stripe Object Mapping

| Glyphoxa Concept | Stripe Object |
|---|---|
| DM (tenant owner) | Customer |
| Subscription plan | Product + Price |
| Active subscription | Subscription |
| Monthly payment | Invoice → PaymentIntent |
| Payment method | PaymentMethod (attached to Customer) |

### Checkout Flow

```
┌──────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────┐
│  Web UI  │     │ Billing API  │     │   Stripe   │     │ Webhook  │
│          │     │              │     │            │     │ Handler  │
└────┬─────┘     └──────┬───────┘     └─────┬──────┘     └────┬─────┘
     │                  │                   │                  │
     │ 1. Click         │                   │                  │
     │   "Subscribe"    │                   │                  │
     ├─────────────────▶│                   │                  │
     │                  │                   │                  │
     │                  │ 2. Create Stripe  │                  │
     │                  │   Checkout Session │                  │
     │                  ├──────────────────▶│                  │
     │                  │                   │                  │
     │                  │ 3. Return         │                  │
     │                  │   checkout URL    │                  │
     │                  │◀──────────────────┤                  │
     │                  │                   │                  │
     │ 4. Redirect to   │                   │                  │
     │   Stripe Checkout│                   │                  │
     │◀─────────────────┤                   │                  │
     │                  │                   │                  │
     ├──────────────────────────────────────▶│                  │
     │         5. Customer enters payment   │                  │
     │◀──────────────────────────────────────┤                  │
     │  6. Redirect to success URL          │                  │
     │                  │                   │                  │
     │                  │                   │ 7. Webhook:      │
     │                  │                   │ checkout.session  │
     │                  │                   │ .completed       │
     │                  │                   ├─────────────────▶│
     │                  │                   │                  │
     │                  │                   │ 8. Webhook:      │
     │                  │                   │ customer         │
     │                  │                   │ .subscription    │
     │                  │                   │ .created         │
     │                  │                   ├─────────────────▶│
     │                  │                   │                  │
     │                  │   9. Create/update │                  │
     │                  │   subscription    │                  │
     │                  │   record          │                  │
     │                  │◀─────────────────────────────────────┤
     │                  │                   │                  │
     │                  │  10. Invalidate   │                  │
     │                  │   cache           │                  │
     │                  │                   │                  │
```

### Webhook Events to Handle

| Stripe Event | Action |
|---|---|
| `checkout.session.completed` | Link Stripe Customer to tenant, create subscription record |
| `customer.subscription.created` | Create/update local subscription, set status `active` |
| `customer.subscription.updated` | Update plan, interval, status, period dates |
| `customer.subscription.deleted` | Set status `cancelled` |
| `invoice.paid` | Record in `payment_history`, clear `past_due` status |
| `invoice.payment_failed` | Set status `past_due`, set `grace_period_end` (+7 days) |
| `customer.subscription.trial_will_end` | (Optional) Send DM a reminder via Discord |

### Webhook Handler

```go
// StripeWebhookHandler processes Stripe webhook events.
type StripeWebhookHandler struct {
    store    BillingStore
    cache    *SubscriptionCache
    secret   string // Stripe webhook signing secret
    logger   *slog.Logger
}

func (h *StripeWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    payload, err := io.ReadAll(r.Body)
    if err != nil { ... }

    event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), h.secret)
    if err != nil {
        http.Error(w, "invalid signature", http.StatusBadRequest)
        return
    }

    switch event.Type {
    case "customer.subscription.created", "customer.subscription.updated":
        h.handleSubscriptionChange(r.Context(), event)
    case "customer.subscription.deleted":
        h.handleSubscriptionDeleted(r.Context(), event)
    case "invoice.paid":
        h.handleInvoicePaid(r.Context(), event)
    case "invoice.payment_failed":
        h.handlePaymentFailed(r.Context(), event)
    }

    w.WriteHeader(http.StatusOK)
}
```

### Idempotency

All webhook handlers are idempotent — processing the same event twice produces the same result. This is achieved by:
- Using `stripe_subscription_id` as a natural key (UPSERT, not INSERT)
- Storing `stripe_invoice_id` with UNIQUE constraint in `payment_history`
- Checking event timestamps against `updated_at` to skip stale events

---

## 6. Payment Failure & Grace Period

### Timeline

```
Day 0: Invoice created, payment attempted
       ├─ Success → invoice.paid → all good
       └─ Failure → invoice.payment_failed
                    ├─ Status → past_due
                    ├─ grace_period_end = now + 7 days
                    └─ Stripe retries automatically (Smart Retries)

Day 1-7: Grace period
          ├─ Sessions still allowed (past_due permits session start)
          ├─ Web UI shows banner: "Payment failed — update your card"
          ├─ DM can update payment method via Stripe Customer Portal
          └─ Stripe retries on days 1, 3, 5

Day 7: Grace period expires
       ├─ If still unpaid → status = suspended
       ├─ Sessions blocked
       └─ Web UI shows: "Subscription suspended — update payment to resume"

Day 30: Final cancellation
        ├─ If still unpaid → status = cancelled
        ├─ Stripe subscription deleted
        └─ Tenant downgraded to Apprentice (free)
```

### Why 7-Day Grace Period

- TTRPG sessions are often weekly. A 7-day grace ensures the DM's next session isn't disrupted by a transient payment failure.
- Stripe's Smart Retries handle re-attempts automatically.
- The DM sees a warning but isn't punished immediately.

---

## 7. Trial Period

### Design

- **14-day trial of Adventurer tier** for new signups
- No credit card required during trial
- Trial starts when tenant is created via web management signup
- At trial end:
  - If card added → convert to paid Adventurer subscription
  - If no card → downgrade to Apprentice (free)
- Trial users get full Adventurer features (8 sessions, standard voices, 10 NPCs)
- One trial per Discord account (tracked by Discord user ID)

### Implementation

```go
func (s *BillingService) CreateTrialSubscription(ctx context.Context, tenantID string) error {
    now := time.Now()
    sub := &Subscription{
        TenantID:            tenantID,
        PlanID:              "adventurer",
        Status:              StatusTrialing,
        BillingInterval:     "monthly",
        CurrentPeriodStart:  now,
        CurrentPeriodEnd:    now.AddDate(0, 0, 14),
        TrialEnd:            ptr(now.AddDate(0, 0, 14)),
    }
    return s.store.CreateSubscription(ctx, sub)
}
```

---

## 8. Tier Upgrade / Downgrade

### Upgrade (Mid-Cycle)

- **Immediate effect.** DM gets higher-tier features right away.
- **Prorated billing.** Stripe calculates the proration automatically.
- Session count resets to 0 for the new tier on the current period.
- Cache invalidated immediately.

### Downgrade (End-of-Cycle)

- **Takes effect at period end.** DM keeps current tier until the billing period expires.
- Set `cancel_at_period_end = false` on old plan (Stripe handles this with `proration_behavior: none`).
- If DM has more NPCs than the new tier allows, they can't create new ones but existing NPCs are preserved (soft limit, not deletion).

### Implementation via Stripe

```go
func (s *BillingService) ChangePlan(ctx context.Context, tenantID, newPlanID string) error {
    sub, err := s.store.GetSubscription(ctx, tenantID)
    if err != nil { return err }

    newPlan, err := s.store.GetPlan(ctx, newPlanID)
    if err != nil { return err }

    oldPlan, err := s.store.GetPlan(ctx, sub.PlanID)
    if err != nil { return err }

    isUpgrade := newPlan.PriceMonthly > oldPlan.PriceMonthly

    params := &stripe.SubscriptionParams{
        Items: []*stripe.SubscriptionItemsParams{{
            ID:    stripe.String(sub.StripeSubscriptionItemID),
            Price: stripe.String(newPlan.StripePriceIDMonthly),
        }},
    }

    if isUpgrade {
        params.ProrationBehavior = stripe.String("create_prorations")
    } else {
        params.ProrationBehavior = stripe.String("none")
        // Downgrade applied at period end via webhook
    }

    _, err = subscription.Update(sub.StripeSubscriptionID, params)
    return err
}
```

---

## 9. Usage Metering & Dashboard

### What's Tracked

The existing `usage_records` table already tracks `session_hours` per tenant per period. The billing layer extends this with the `billing_events` table for per-session granularity.

| Metric | Source | Storage |
|---|---|---|
| Sessions used this period | Count of `billing_events` with `event_type='session_start'` | `billing_events` |
| Total session hours | Sum of `billing_events.session_minutes / 60` | `billing_events` |
| NPCs active | Count from `npcstore` per campaign | Live query |
| Current plan | `subscriptions.plan_id` | `subscriptions` |
| Payment status | `subscriptions.status` | `subscriptions` |

### BillingRecorder (Session End Hook)

Wraps the existing `RecordingBridge` to capture billing-specific data:

```go
type BillingRecorder struct {
    inner    gateway.GatewayCallback
    orch     sessionorch.Orchestrator
    billing  BillingStore
}

func (b *BillingRecorder) ReportState(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
    if state == gateway.SessionEnded {
        sess, err := b.orch.GetSession(ctx, sessionID)
        if err == nil {
            duration := time.Since(sess.StartedAt)

            // Don't count sessions < 60 seconds (accidental starts)
            if duration >= 60*time.Second {
                sub, _ := b.billing.GetSubscription(ctx, sess.TenantID)
                planID := "apprentice"
                if sub != nil {
                    planID = sub.PlanID
                }

                event := BillingEvent{
                    TenantID:       sess.TenantID,
                    SessionID:      sessionID,
                    EventType:      "session_end",
                    SessionMinutes: duration.Minutes(),
                    PlanID:         planID,
                    Period:         currentPeriod(),
                }
                b.billing.RecordBillingEvent(ctx, event)
            }
        }
    }

    return b.inner.ReportState(ctx, sessionID, state, errMsg)
}
```

### Usage Dashboard API

```
GET /api/v1/billing/usage?tenant_id={id}&period=2026-03
→ {
    "plan": "adventurer",
    "sessions_used": 5,
    "sessions_cap": 8,
    "total_hours": 14.5,
    "npcs_active": 7,
    "npcs_cap": 10,
    "period_start": "2026-03-01T00:00:00Z",
    "period_end": "2026-03-31T23:59:59Z",
    "status": "active",
    "sessions": [
        {
            "id": "sess_abc123",
            "started_at": "2026-03-15T19:00:00Z",
            "duration_minutes": 185,
            "campaign": "Curse of Strahd"
        }
    ]
}
```

---

## 10. Self-Hosted vs SaaS

### Deployment Mode Detection

The binary already supports `--mode=full|gateway|worker`. Self-hosted vs SaaS is orthogonal — it's determined by a build flag and config:

```go
// config.yaml
deployment:
  mode: full           # full | gateway | worker
  hosting: selfhosted  # selfhosted | managed
  license_key: ""      # optional, for premium self-hosted features
```

### Feature Matrix

| Feature | Self-Hosted (Free) | Self-Hosted (Licensed) | SaaS (Managed) |
|---|---|---|---|
| Voice pipeline (VAD→STT→LLM→TTS) | Yes (own keys) | Yes (own keys) | Yes (included) |
| NPC creation & management | Yes | Yes | Yes |
| Discord bot integration | Yes | Yes | Yes |
| Web management UI | Yes (local) | Yes (local) | Yes (hosted) |
| Knowledge graph | Yes (own PostgreSQL) | Yes | Yes (tier-gated) |
| Custom voice cloning | No | Yes | Guild tier only |
| Priority support | No | Yes | Guild tier only |
| Automatic updates | No | Yes | Yes |
| Multi-tenant gateway | No | Yes | Yes |
| Session analytics | Basic | Full | Full |
| Subscription billing | N/A | N/A | Yes (Stripe) |

### License Key System (Self-Hosted Premium)

For self-hosted users who want premium features without the managed service:

```go
// License key is a signed JWT with claims:
type LicenseClaims struct {
    TenantID   string    `json:"tid"`
    Features   []string  `json:"features"` // ["voice_cloning", "priority_support", "analytics"]
    ExpiresAt  time.Time `json:"exp"`
    IssuedAt   time.Time `json:"iat"`
}
```

- Keys are generated by the Glyphoxa admin dashboard
- Validated offline (no phone-home) — public key embedded in binary
- Expiry checked at startup and periodically (daily)
- Grace period: 30 days after expiry before features are disabled

### Code-Level Differentiation

```go
// internal/billing/mode.go

type DeploymentMode int

const (
    ModeSelfHostedFree DeploymentMode = iota
    ModeSelfHostedLicensed
    ModeManaged
)

func (m DeploymentMode) RequiresSubscription() bool {
    return m == ModeManaged
}

func (m DeploymentMode) HasFeature(feature string) bool {
    switch m {
    case ModeSelfHostedFree:
        return false // Only core features
    case ModeSelfHostedLicensed:
        return true // License claims checked separately
    case ModeManaged:
        return true // Plan-gated via subscription
    }
    return false
}
```

The `BillingAuthorizer` checks deployment mode first:

```go
func (a *BillingAuthorizer) ValidateAndCreate(ctx context.Context, req SessionRequest) (string, error) {
    if !a.mode.RequiresSubscription() {
        // Self-hosted: skip billing checks, delegate to QuotaGuard directly
        return a.inner.ValidateAndCreate(ctx, req)
    }

    // Managed: full subscription + plan validation
    sub, plan, err := a.cache.Get(ctx, req.TenantID)
    // ...check status, session count, NPC count, model/voice tier...

    return a.inner.ValidateAndCreate(ctx, req)
}
```

---

## 11. Billing API Endpoints

### Subscription Management

```
POST   /api/v1/billing/checkout
       → Create Stripe Checkout session, return URL
       Body: { "tenant_id": "...", "plan_id": "adventurer", "interval": "monthly" }

GET    /api/v1/billing/subscription?tenant_id={id}
       → Current subscription details + plan info

POST   /api/v1/billing/subscription/change
       → Upgrade or downgrade plan
       Body: { "tenant_id": "...", "new_plan_id": "dungeon_master" }

POST   /api/v1/billing/subscription/cancel
       → Cancel at end of current period
       Body: { "tenant_id": "..." }

POST   /api/v1/billing/subscription/reactivate
       → Undo pending cancellation
       Body: { "tenant_id": "..." }

GET    /api/v1/billing/portal?tenant_id={id}
       → Create Stripe Customer Portal session, return URL
       (self-service: update card, view invoices, etc.)
```

### Usage & History

```
GET    /api/v1/billing/usage?tenant_id={id}&period=2026-03
       → Session count, hours, NPC count for period

GET    /api/v1/billing/payments?tenant_id={id}&limit=10
       → Payment history from payment_history table
```

### Stripe Webhook

```
POST   /api/v1/billing/stripe/webhook
       → Stripe webhook endpoint (signature-verified)
```

---

## 12. Wiring Into the Gateway

### Integration Point: `cmd/glyphoxa/main.go`

The billing layer inserts between the existing `QuotaGuard` and the `GatewaySessionController`. Minimal changes to `runGateway()`:

```go
func runGateway(ctx context.Context, cfg *config.Config) error {
    // ... existing setup ...

    // Existing orchestrator + quota guard
    orch := sessionorch.NewPostgresOrchestrator(db)
    usageStore := usage.NewPostgresStore(db)
    quotaGuard := usage.NewQuotaGuard(orch, usageStore, tenantQuotaLookup)

    // NEW: Billing layer wraps QuotaGuard
    var sessionAuth sessionorch.Orchestrator
    if cfg.Deployment.Hosting == "managed" {
        billingStore := billing.NewPostgresStore(db)
        subCache := billing.NewSubscriptionCache(billingStore, 5*time.Minute)
        billingAuth := billing.NewBillingAuthorizer(quotaGuard, subCache)
        sessionAuth = billingAuth

        // Stripe webhook handler
        stripeHandler := billing.NewStripeWebhookHandler(billingStore, subCache, cfg.Stripe.WebhookSecret)
        mux.Handle("POST /api/v1/billing/stripe/webhook", stripeHandler)

        // Billing API
        billingAPI := billing.NewAPI(billingStore, subCache, cfg.Stripe.SecretKey)
        billingAPI.Register(mux)
    } else {
        sessionAuth = quotaGuard
    }

    // NEW: BillingRecorder wraps existing RecordingBridge
    var callback gateway.GatewayCallback
    recordingBridge := usage.NewRecordingBridge(gatewayCallback, orch, usageStore)
    if cfg.Deployment.Hosting == "managed" {
        callback = billing.NewBillingRecorder(recordingBridge, orch, billingStore)
    } else {
        callback = recordingBridge
    }

    // ... rest of setup uses sessionAuth instead of quotaGuard ...
}
```

### Config Addition

```yaml
# config.yaml — new billing section
stripe:
  secret_key: "${STRIPE_SECRET_KEY}"
  webhook_secret: "${STRIPE_WEBHOOK_SECRET}"
  publishable_key: "${STRIPE_PUBLISHABLE_KEY}"  # passed to frontend

deployment:
  hosting: managed  # or selfhosted
```

---

## 13. Package Layout

```
internal/billing/
├── api.go                  # HTTP handlers for billing endpoints
├── authorizer.go           # BillingAuthorizer (wraps QuotaGuard)
├── cache.go                # SubscriptionCache (in-memory TTL)
├── mode.go                 # DeploymentMode (selfhosted/managed)
├── models.go               # Subscription, SubscriptionPlan, BillingEvent, etc.
├── recorder.go             # BillingRecorder (wraps RecordingBridge)
├── store.go                # BillingStore interface
├── store_postgres.go       # PostgreSQL implementation
├── stripe_webhook.go       # Stripe webhook handler
├── stripe_checkout.go      # Stripe Checkout session creation
├── migrations/
│   └── 000001_billing.up.sql
│   └── 000001_billing.down.sql
└── mock/
    └── store.go            # Mock for testing
```

---

## 14. Implementation Plan

### Phase 1: Foundation (Week 1-2)

1. Create `internal/billing/` package with models and store interface
2. Write database migrations (`subscription_plans`, `subscriptions`, `billing_events`, `payment_history`)
3. Implement `BillingStore` (PostgreSQL)
4. Implement `SubscriptionCache`
5. Seed `subscription_plans` table with 4 tiers
6. Write unit tests with mock store

### Phase 2: Stripe Integration (Week 2-3)

1. Set up Stripe account, create Products + Prices for each tier
2. Implement `StripeWebhookHandler` with event processing
3. Implement Stripe Checkout session creation
4. Implement Stripe Customer Portal integration
5. Wire webhook endpoint into gateway HTTP mux
6. Test with Stripe CLI (`stripe listen --forward-to`)

### Phase 3: Authorization Gate (Week 3-4)

1. Implement `BillingAuthorizer` wrapper
2. Implement `BillingRecorder` wrapper
3. Wire both into `cmd/glyphoxa/main.go` gateway startup
4. Implement deployment mode detection (`selfhosted` vs `managed`)
5. Integration test: session start → billing check → session created/rejected
6. Test grace period and suspension flows

### Phase 4: Billing API (Week 4-5)

1. Implement billing REST API endpoints
2. Usage dashboard endpoint with session history
3. Plan change (upgrade/downgrade) endpoint
4. Cancellation and reactivation endpoints
5. Wire into web management UI (React components)

### Phase 5: Trial & Polish (Week 5-6)

1. Implement 14-day trial flow
2. Add session length enforcement (timer-based caps)
3. Add NPC count enforcement in npcstore
4. Discord notifications for billing events (payment failed, trial ending)
5. License key validation for self-hosted premium
6. End-to-end testing across all tiers

---

## 15. Error Messages

User-facing errors returned to Discord when `/session start` is rejected:

| Condition | Discord Response |
|---|---|
| Session cap reached | "You've used all {cap} sessions this month. Upgrade your plan at {url} or wait until {period_end}." |
| Subscription suspended | "Your subscription is suspended due to a payment issue. Update your payment method at {url}." |
| Subscription cancelled | "Your subscription has been cancelled. Resubscribe at {url} to start sessions." |
| NPC limit exceeded | "Your plan allows {max} NPCs. Remove some NPCs or upgrade at {url}." |
| No subscription (managed) | "You need a Glyphoxa subscription to start sessions. Sign up at {url} — free tier available!" |
| Trial expired | "Your free trial has ended. Subscribe at {url} to keep using Glyphoxa — plans start free!" |

---

## 16. Open Questions

1. **Session length enforcement UX** — Should the 5-minute warning be a DM-only whisper or announced to the voice channel? Lean toward DM-only to avoid breaking immersion.

2. **Free tier abuse** — Multiple Discord accounts to farm free sessions? Mitigation: tie free tier to Discord account age (>30 days) or require email verification.

3. **Group billing (Guild tier)** — Do the 5 "player seats" mean 5 additional DMs who can start sessions, or 5 players who get their own web dashboard? Lean toward 5 DM seats (co-DM model).

4. **Session pack add-ons** — Should Adventurer users be able to buy extra sessions without upgrading? Could be a nice middle ground — $2 per additional session.

5. **Currency** — USD only at launch? EUR for European market? Stripe handles multi-currency, but pricing page needs thought.

6. **Tax handling** — Stripe Tax for automated VAT/sales tax, or handle manually? Stripe Tax recommended for simplicity.
