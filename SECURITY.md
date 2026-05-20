# Security Policy

## Supported versions

`clew` is in early-stage PoC development. Only the **latest released minor
version** receives security fixes.

| Version | Supported |
|---|---|
| 0.1.x   | ✅        |
| < 0.1   | ❌        |

## Reporting a vulnerability

**Please do not file public GitHub issues for security vulnerabilities.**

The preferred channel is GitHub's private vulnerability reporting:

  → <https://github.com/nishikawaakira/clew/security/advisories/new>

If that link is unavailable (e.g. private vulnerability reporting has not
been enabled yet for this repository), you can reach the maintainer via the
email address shown on their GitHub profile with `[clew security]` in the
subject line.

### What to include

- A description of the issue and the impact you observed.
- Steps to reproduce, ideally with a minimal example.
- Affected version(s) of `clew`.
- (Optional) A proposed fix or mitigation.

### Response targets

These are best-effort timelines for a hobby-scale project — please be
patient if a reply is delayed.

- **Acknowledgment of report**: within 7 days.
- **Initial triage / assessment**: within 14 days.
- **Fix or coordinated disclosure**: within 90 days for confirmed issues.

## Out of scope

- Vulnerabilities surfaced by Dependabot in transitive Go module
  dependencies are tracked automatically; please file these as regular
  issues unless you have a working exploit specifically through clew's
  usage of the affected library.
- The HTML output loads Cytoscape.js / dagre / cytoscape-dagre from a
  public CDN at view time. CDN-side incidents are not in scope for this
  policy — keep using a trusted browser and network.
- AWS account / IAM misconfigurations exposed *by* the diagrams produced
  by `clew`. The tool only visualises what's already in your AWS Config
  snapshot; treat its output with the same sensitivity as the snapshot
  itself.
