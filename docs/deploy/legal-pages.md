# Legal pages: Impressum, Datenschutzerklärung, Nutzungsbedingungen

The SPA serves three static, **unauthenticated** documents, linked from a footer
on every screen (including login and signup):

| Route | Aliases | Document |
|-------|---------|----------|
| `/imprint` | `/impressum` | Impressum (§ 5 DDG) |
| `/privacy` | `/datenschutz` | Datenschutzerklärung |
| `/terms` | `/nutzungsbedingungen` | Nutzungsbedingungen / AUP |

In the **open** Admission Mode (ADR-0055) the login screen — which is the signup
screen — additionally requires the visitor to acknowledge the
Nutzungsbedingungen and the Datenschutzerklärung before the Discord OAuth flow
can start. The gate is enforced **server-side**, not just rendered: an
open-mode `GET /auth/discord/login` without `aup=1` is bounced back to the
login screen, the acknowledgment rides the OAuth state cookie to the callback,
an open-mode signup whose round trip lacks it writes nothing, and the
acceptance time is recorded on the user row (`users.aup_accepted_at`) — the
§ 305 BGB inclusion evidence. Allowlist-mode logins are not gated.

`/privacy` is also a deployment contract: the Helm chart derives
`GLYPHOXA_PRIVACY_POLICY_URL` as `<ingress host>/privacy`, and the Bot links it
from the transcription disclosure it posts in the voice channel at Voice Session
start (#519). Keep the route as it is, or set `privacyPolicyUrl` explicitly.

> **These texts are templates, not legal advice.** They were drafted from
> established German boilerplate against what the software actually does, and
> they have **not** been reviewed by a lawyer — an accepted beta risk decided on
> 2026-07-22 (#518). If you operate this for other people, have them reviewed.

## Operator TODO — before the deployment is reachable

The operator's identity is **not** something this project can supply. It lives
in [`web/src/screens/legal/operator.ts`](../../web/src/screens/legal/operator.ts),
which ships filled in with the identity of the canonical glyphoxa.com
deployment — **if you deploy your own instance, every value there is wrong for
you and has to be replaced.** A field you have not answered is either empty or
carries a `[BITTE AUSFÜLLEN: …]` placeholder; while any required one is in that
state, every legal page renders a red "Hinweis an den Betreiber" banner naming
the missing fields, and each placeholder value is highlighted inline — an
unfinished Impressum is meant to be impossible to miss.

1. Edit `web/src/screens/legal/operator.ts` and fill in:
   - `legalName`, `street`, `city`, `country` — § 5 DDG requires a real,
     summonable postal address (no P.O. box);
   - `email` — a contact that reaches a human quickly;
   - `supervisoryAuthority` — the data-protection authority of your federal
     state (Art. 77 GDPR complaint route);
   - `hostingLocation` — where the database physically runs. The
     Datenschutzerklärung repeats this claim, so it must be true;
   - `lastUpdated` — the date you reviewed the texts.
   Optional: `phone`, `contentResponsible` (§ 18 Abs. 2 MStV),
   `vatId` (§ 27a UStG), `dataProtectionOfficer` (most beta-scale operators do
   not need one — Art. 37 GDPR). Leave them empty to omit those lines.
   - `edgeProvider` — the reverse proxy or CDN your web tier is reached
     **through**, if any (e.g. `"Cloudflare"` when you run the chart's
     `cloudflared.enabled` tunnel or a `cloudflared` compose service). Such a
     provider terminates TLS, so it sees connection data *and* the web traffic
     in the clear: §5 names it as a recipient, and states that Voice Session
     audio does not cross it (that goes straight to Discord). **Leave it empty
     if you are reached directly or only through your own proxy** — the section
     then names no such provider at all. An empty value is a complete answer,
     not a TODO, and never raises the banner.
2. Read the Datenschutzerklärung end to end and correct anything that does not
   match YOUR deployment — in particular §5 (which AI providers you actually
   use) and §6 (where the data lives). The template names the shipped adapters
   (Discord, ElevenLabs, Groq, Anthropic, Google Gemini, OpenAI-compatible
   endpoints) plus whatever `edgeProvider` you declared — cross-check against
   your configured providers, and remember an "OpenAI-compatible endpoint" is
   whatever vendor you point it at.
3. Rebuild the SPA (`npm run build` in `web/`, or rebuild the container image —
   the pages are compiled into the bundle) and deploy.
4. Verify before you publish DNS. Ask the identity itself, from `web/`:

   ```sh
   npm run check:operator
   ```

   That runs the very `operatorTodos()` the pages call for their banner, over
   your `operator.ts`, and exits non-zero naming every field that is still a
   placeholder **or empty** — plus any optional field left on template text,
   which renders red without raising the banner. It must exit 0.

   Then confirm the bundle you are shipping actually carries that identity.
   The legal pages are client-rendered, so fetching `/imprint` only returns the
   SPA shell — check the **built bundle**, not the HTML:

   ```sh
   # locally, from the repo root, against the build you are about to ship (vite
   # outDir). Take the bundle index.html references: emptyOutDir is false, so
   # dist/assets/ keeps earlier builds and a stale sibling would answer for it.
   js=$(grep -o '/assets/[^"]*\.js' internal/spa/dist/index.html | head -1)
   grep -q "BITTE AUSFÜLLEN:" "internal/spa/dist$js" && echo "STILL UNFILLED"

   # or against the deployed instance (fetches the hashed JS the shell references):
   host=https://<your-host>
   js=$(curl -sf "$host/" | grep -o '/assets/[^"]*\.js' | head -1)
   curl -sf "$host$js" | grep -q "BITTE AUSFÜLLEN:" && echo "STILL UNFILLED"
   curl -sf "$host$js" | grep -q "<your legalName>" || echo "STALE BUNDLE"
   ```

   Neither may print `STILL UNFILLED`, and the live bundle must contain your own
   `legalName` — a bundle built before you edited `operator.ts` still serves the
   previous operator's Impressum, which is the failure step 3 exists to prevent.

   Grep for the **colon** form. An unfilled value reads `[BITTE AUSFÜLLEN: …]`,
   whereas the bare `[BITTE AUSFÜLLEN` prefix that `isPlaceholder()` compares
   against is a constant compiled into *every* bundle: matching on it — as this
   step did until 2026-08-06 — reports `STILL UNFILLED` on a perfectly filled
   deployment, which is worse than no check at all. And no grep over a minified
   bundle can see a required field left empty; that is what `check:operator` is
   for, and why it, not the grep, is the check that decides.

## Keeping the texts honest

The Datenschutzerklärung describes real data flows. When any of these change,
the text has to change with them:

- **Discord OAuth fields** (`internal/auth/discord.go`) — §2.
- **Transcription** — Transcript Lines and Chunks plus their embeddings
  (ADR-0011 / ADR-0040) — §3. The in-channel disclosure the Bot posts (#519)
  says the same thing in one sentence; keep them consistent.
- **The Rollover Tape** — consent model, 120 s window, 7-day candidate purge
  (ADR-0051) — §4.
- **Providers** (ADR-0004) — §5. Adding a provider means adding a subprocessor.
- **Retention** — §7.

`web/src/screens/legal/legal.test.tsx` pins the topics the Datenschutzerklärung
must mention, so dropping one fails the web test suite rather than shipping a
privacy policy that no longer describes the product.

## See also

- [k3s-proxmox.md](k3s-proxmox.md) — the home-lab deployment these pages ship with.
- [saas-operations.md](saas-operations.md) — the go-live checklist.
- [ADR-0055](../adr/0055-self-signup-admission-modes.md) — the open Admission
  Mode whose signup carries the AUP acknowledgment.
