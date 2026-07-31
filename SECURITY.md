# Security policy

Inroad handles mailbox credentials and outbound email, so security reports are
treated as a priority.

## Reporting a vulnerability

Do not open a public issue. Use the repository's private
[security advisory form](https://github.com/Axomble/Inroad/security/advisories/new)
and include:

- the affected version or commit;
- reproduction steps or a minimal proof of concept;
- the expected and observed impact;
- any suggested mitigation.

You should receive an acknowledgement within 72 hours. We will coordinate a
fix and disclosure timeline with you and credit reporters who want attribution.

## Supported versions

Until tagged stable releases begin, security fixes are applied to the latest
commit on `main`. Self-hosters should keep deployments current and subscribe to
repository security advisories.

## Deployment responsibility

Use unique production secrets, TLS at the public edge, a non-default Postgres
password, and backups appropriate for your environment. The non-negotiable
application invariants are documented in [docs/security.md](docs/security.md).
