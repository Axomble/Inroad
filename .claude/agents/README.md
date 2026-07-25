# Inroad subagents

Project-level Claude Code subagents. Each is a Markdown file with YAML frontmatter;
the `tools:` line is the hard boundary — an agent cannot use a tool it isn't granted.

## Roles

| Agent | Writes code? | Purpose | Model |
|-------|:---:|---------|-------|
| **developer** | ✅ **only writer** | Implements features, fixes bugs. TDD, project conventions, security invariants. | inherit |
| **reviewer** | ❌ read-only | Reviews the branch diff for correctness, layering, conventions, maintainability. | opus |
| **qa** | ❌ read-only | Runs Go/Vitest/Playwright suites, reports pass/fail + coverage gaps. | sonnet |
| **security** | ❌ read-only | Audits creds/auth/tenant/SSRF against `docs/security.md` + threats. | opus |
| **performance** | ❌ read-only | Audits hot paths, DB queries, worker loops, SPA bundle. | opus |

Only **developer** has `Write`/`Edit`. The four auditors report findings for the
developer to act on — enforced structurally, not by convention.

## Intended workflow

```
spec/plan ──▶ developer (implements, TDD)
                 │
                 ├──▶ reviewer      ┐
                 ├──▶ security       │ read-only audits, run in parallel
                 ├──▶ performance   ┘
                 └──▶ qa            (runs the suites + E2E)
                          │
                 findings ▼
                 developer (applies fixes) ──▶ re-audit as needed
```

## Invoking

- Explicit: ask the main session to "use the **reviewer** agent on this change" (or security / performance / qa / developer).
- The auditors are read-only and independent, so they can be dispatched **in parallel** in a single message.
- Each agent invokes the relevant Superpowers / code-review / Postgres / frontend skills itself (they have the `Skill` tool); it will also read `CLAUDE.md`, `CONTRIBUTING.md`, and `docs/security.md` for project context.

## Skills each agent leans on

- **developer:** `superpowers:test-driven-development`, `systematic-debugging`, `executing-plans`, `verification-before-completion`; `frontend-design`/`ui-ux-pro-max` for UI; `supabase:supabase-postgres-best-practices` for queries.
- **reviewer:** `code-review:code-review`; `ui-ux-pro-max`/`frontend-design`; Postgres best practices.
- **qa:** `superpowers:verification-before-completion`, `systematic-debugging`, `test-driven-development`.
- **security:** `security-review`; Postgres best practices.
- **performance:** `supabase:supabase-postgres-best-practices`.

> Note: this project uses **pgx/sqlc**, not Supabase — use only the generic Postgres
> guidance from that skill, ignore Supabase-product-specific parts.
