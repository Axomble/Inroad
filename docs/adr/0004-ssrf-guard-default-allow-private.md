# 0004 — SSRF guard on mailbox connection tests; private hosts allowed by default

**Status:** Accepted

## Context
Connecting a mailbox runs an SMTP/IMAP connection test against a user-supplied
host. Any authenticated user could point it at internal addresses — the cloud
metadata endpoint (`169.254.169.254`), loopback services, or internal hosts — and
use the tester as an SSRF probe. But self-hosted operators legitimately connect to
internal mail servers on private IPs (PRD §9.1.3), so a blanket private-IP block
would break the primary self-host persona out of the box.

## Decision
Route every user-supplied dial through `mail.vetAddr`:
- **Always block** loopback, link-local (incl. metadata), unspecified, multicast.
- **Private RFC1918/ULA:** blocked unless `INROAD_MAIL_ALLOW_PRIVATE_HOSTS=true` —
  default **true** for self-hosted Core, set **false** for multi-tenant Cloud.
- Enforce a mail-port allowlist (SMTP 25/465/587/2525, IMAP 143/993).
- Dial the resolved IP (hostname kept as TLS ServerName) to close DNS rebinding.

## Consequences
- The dangerous SSRF targets (metadata, localhost) are blocked for everyone,
  including with private hosts allowed.
- Self-hosters reach internal mail servers by default; Cloud flips one env var to
  the strict posture.
- Non-standard mail ports are rejected until an explicit per-mailbox override is
  added (future).
