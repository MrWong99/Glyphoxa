# Security Policy

## Supported Versions

Glyphoxa is in public beta and has not reached 1.0. Security fixes land on
`main` and ship in the next `vX.Y.Z` release image
(`ghcr.io/mrwong99/glyphoxa`); there are no backports to older releases.
Releases below v0.16.0 have known defects and must not be run.

| Version | Supported |
|---------|-----------|
| Latest release + `main` | ✅ |
| Older releases | ❌ |

## Reporting a Vulnerability

**Please do not open a public issue for security vulnerabilities.**

Instead, report vulnerabilities privately:

1. **GitHub Security Advisories** (preferred) — go to
   [Security → Advisories → New draft advisory](https://github.com/MrWong99/Glyphoxa/security/advisories/new)
2. **Email** — send details to the repository owner via their GitHub profile

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if you have one)

### Response Timeline

- **Acknowledgement** — within 48 hours
- **Assessment** — within 1 week
- **Fix** — depends on severity; critical issues are prioritised immediately

## Scope

The following are in scope for security reports:

- Authentication/authorization bypasses
- Remote code execution
- Data exfiltration via provider interfaces
- Credential leakage in logs or error messages
- Denial of service via malformed audio/protocol data

The following are **out of scope**:

- Issues in third-party dependencies (report upstream)
- Social engineering attacks
- Issues requiring physical access to the server
