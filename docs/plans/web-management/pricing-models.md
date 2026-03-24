# Pricing Models for AI-Powered TTRPG Tools

**Date:** 2025-02-25  
**Author:** Research Agent

## Key Findings

- **The TTRPG market expects $5-15/month** for AI tools — comparable to VTT subscriptions.
- **Usage-based pricing is poorly received** by TTRPG players — they want predictable costs for sessions that can run 3-6 hours.
- **AI Dungeon's tiered model** ($10-30/month with feature gating) is the closest analog.
- **Glyphoxa's actual costs will be dominated by LLM + TTS** — roughly $0.50-3.00 per hour depending on configuration.
- **The community strongly dislikes per-message/per-interaction pricing** — it creates anxiety about "using up" interactions during roleplay.
- **A hybrid model** (flat subscription + usage cap with overage) is the most viable approach.

## Existing Product Pricing

### AI Dungeon (Latitude)
The closest comparison — AI-powered interactive fiction.

| Tier | Price/Month | Key Features |
|------|-------------|--------------|
| Free | $0 | Basic models, 4k context, limited usage |
| Champion | ~$10 | Premium models, 8k context, 480 credits/mo |
| Legend | ~$20 | Better models, 16k context, 950 credits/mo |
| Mythic | ~$30 | Best models (GPT-4), 32k context, 2400 credits/mo |

- Credits used for image generation and "Ultra context"
- Text generation itself is unlimited at tier's model level
- **Key insight:** They gate by model quality and context, not by message count

### Character.AI
| Tier | Price/Month | Key Features |
|------|-------------|--------------|
| Free | $0 | Basic chat, slower during peak, limited models |
| c.ai+ | $9.99/mo ($120/yr) | Priority access, faster responses, voice calls, advanced models |

- Simple two-tier model
- Voice calls are a premium feature (relevant for Glyphoxa)
- **Key insight:** Voice is a premium differentiator worth paying for

### Inworld AI (Game Developer Platform)
| Component | Price |
|-----------|-------|
| TTS Standard | $5/M characters |
| TTS Premium | $10/M characters |
| Character interactions | Usage-based (custom pricing) |
| Free tier | Available for development |

- Enterprise/developer-focused, not consumer
- **Key insight:** ~$5-10 per million TTS characters = ~$0.01-0.02 per NPC response (50-100 chars)

### Convai (Game Developer Platform)
| Tier | Price | Key Features |
|------|-------|--------------|
| Free | $0 | 100 daily interactions, non-production |
| Indie | Custom | Higher limits, production use |
| Enterprise | Custom | Unlimited, priority support |

- Developer-focused, interaction-based
- Free tier generous enough for testing

### LitRPG Adventures
- TTRPG content generator (text, not voice)
- ~$10/month for AI-generated backstories, dungeons, quests
- **Key insight:** TTRPG-specific tools cluster around $5-15/month

### Other TTRPG AI Tools
| Product | Price | Type |
|---------|-------|------|
| AI Game Master | ~$10/month | Text RPG |
| AI Realm | Free/Premium | D&D-inspired AI GM |
| RPG.AI | ~$5-10/month | NPC conversation |

## TTRPG Community Sentiment on Pricing

Based on Reddit discussions, TTRPG forums, and product reviews:

### What Players Accept
- **$5-15/month** for a useful AI tool (comparable to D&D Beyond, Roll20 Pro)
- **Flat rate preferred** — "I just want to pay and not think about it during the session"
- **Session-based pricing** could work: "$1-2 per session" instead of monthly
- **Group/party pricing** appreciated: one subscription covers the whole table
- **Free tier** is essential for trial and adoption

### What Players Reject
- **Per-message/per-interaction pricing** — creates anxiety, breaks immersion
- **>$20/month** for a single tool (that's their entire VTT + content budget)
- **Metered usage that could "run out" mid-session** — worst possible UX
- **Surprise costs** — "I ran a 6-hour session and it cost $15" is unacceptable

### DM vs Player Pricing
- DMs are willing to pay more (~$15-20/month) since they get the most value
- Players expect $0-5/month additional cost
- **"DM pays, players benefit"** is the natural model for TTRPG tools

## Cost Analysis for Glyphoxa

### Per-Hour Cost Estimates (Infrastructure)

| Component | Low (Budget) | Medium (Standard) | High (Premium) |
|-----------|-------------|-------------------|----------------|
| STT (Deepgram) | $0.10/hr | $0.10/hr | $0.10/hr |
| LLM (text gen) | $0.05/hr (Flash) | $0.20/hr (GPT-4o-mini) | $1.00/hr (GPT-4o) |
| TTS | $0.05/hr (basic) | $0.20/hr (ElevenLabs) | $0.50/hr (premium voices) |
| Knowledge graph | Negligible | Negligible | Negligible |
| **Total** | **~$0.20/hr** | **~$0.50/hr** | **~$1.60/hr** |

### Session Cost (4 hours)
- Budget: ~$0.80/session
- Standard: ~$2.00/session
- Premium: ~$6.40/session

### Monthly Cost (4 sessions/month)
- Budget: ~$3.20/month
- Standard: ~$8.00/month
- Premium: ~$25.60/month

## Proposed Pricing Models for Glyphoxa

### Model A: Tiered Subscription (Recommended)

| Tier | Price/Month | Features | Target |
|------|-------------|----------|--------|
| **Apprentice** (Free) | $0 | 2 sessions/month, basic voice, 2 NPCs max, Gemini Flash | Trial/evaluation |
| **Adventurer** | $9/month | 8 sessions/month, standard voices, 10 NPCs, GPT-4o-mini | Casual DMs |
| **Dungeon Master** | $19/month | Unlimited sessions, premium voices, unlimited NPCs, GPT-4o, knowledge graph | Serious DMs |
| **Guild** | $29/month | Everything + 5 player seats, priority support, custom voice training | Groups |

### Model B: Session Packs
- 5-session pack: $5 (~$1/session)
- 20-session pack: $15 (~$0.75/session)
- Unlimited monthly: $19
- **Pros:** Pay-as-you-go without per-message anxiety
- **Cons:** Less predictable revenue

### Model C: Self-Hosted (Open Core)
- Open source core (bring your own API keys)
- Paid cloud service for convenience
- **Pros:** Community adoption, trust, no pricing anxiety
- **Cons:** Revenue limited to hosted service and premium features

## Sources

- https://play.aidungeon.com/pricing
- https://help.aidungeon.com/memberships-benefits
- https://latitude.io/blog/more-value-more-tiers-our-first-subscription-change-in-years
- https://characteraiold.com/pricing/
- https://www.eesel.ai/blog/character-ai-pricing
- https://www.eesel.ai/blog/inworld-ai-pricing
- https://convai.com/pricing
- https://aihungry.com/tools/convai/pricing
- https://www.litrpgadventures.com/
- https://airealm.com/

## Recommendations for Glyphoxa

1. **Start with Model A (Tiered Subscription).** The TTRPG community expects flat-rate pricing. Gate by quality (model tier, voice quality, NPC count) rather than usage (messages, minutes).

2. **Never charge per-message or per-interaction.** This kills immersion and creates the worst possible UX for a roleplay tool. Players should never think "should I talk to this NPC or save my credits?"

3. **Price the "Adventurer" tier at $9/month** — this is the sweet spot that matches existing TTRPG tool pricing (D&D Beyond, Roll20 Pro) and covers standard-tier infrastructure costs with margin.

4. **Consider the "DM pays" model.** The DM runs Glyphoxa; players just talk to NPCs. Price accordingly — only the DM needs a subscription.

5. **Offer an annual discount** (e.g., $9/month or $90/year = 2 months free) to improve retention and cash flow.

6. **The self-hosted option (Model C) should be considered seriously.** TTRPG players are technical and privacy-conscious. An open-source core that works with your own API keys, plus a paid hosted version, could drive adoption and community goodwill.

7. **Set hard session caps, not soft ones.** "8 sessions/month" is better than "X minutes of audio" — sessions are a natural unit for TTRPG players and are predictable.

8. **Monitor actual usage costs carefully** before launch. The cost estimates above are approximations — real-world TTRPG sessions may use more or less LLM/TTS than predicted depending on player interaction patterns.

