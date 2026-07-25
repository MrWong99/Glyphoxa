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
can start.

`/privacy` is also a deployment contract: the Helm chart derives
`GLYPHOXA_PRIVACY_POLICY_URL` as `<ingress host>/privacy`, and the Bot links it
from the transcription disclosure it posts in the voice channel at Voice Session
start (#519). Keep the route as it is, or set `privacyPolicyUrl` explicitly.

> **These texts are templates, not legal advice.** They were drafted from
> established German boilerplate against what the software actually does, and
> they have **not** been reviewed by a lawyer — an accepted beta risk decided on
> 2026-07-22 (#518). If you operate this for other people, have them reviewed.

## Operator TODO — before the deployment is reachable

The operator's identity is **not** something this project can supply, so it
ships as clearly-marked placeholders in
[`web/src/screens/legal/operator.ts`](../../web/src/screens/legal/operator.ts).
While any required field is unfilled, every legal page renders a red
"Hinweis an den Betreiber" banner naming the missing fields, and each unfilled
value is highlighted inline — an unfinished Impressum is meant to be impossible
to miss.

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
2. Read the Datenschutzerklärung end to end and correct anything that does not
   match YOUR deployment — in particular §5 (which AI providers you actually
   use) and §6 (where the data lives). The template lists every provider the
   software *can* use.
3. Rebuild the SPA (`npm run build` in `web/`, or rebuild the container image —
   the pages are compiled into the bundle) and deploy.
4. Verify the pages before you publish DNS:

   ```sh
   curl -sf https://<your-host>/imprint  | grep -q "BITTE AUSFÜLLEN" && echo "STILL UNFILLED"
   curl -sf https://<your-host>/privacy  >/dev/null && echo "privacy page served"
   ```

   The first command must print nothing.

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
