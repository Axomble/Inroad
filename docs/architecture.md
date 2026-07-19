# Inroad Architecture

See `docs/superpowers/specs/2026-07-19-outpost-repo-architecture-design.md` for the
full layout rationale. This document tracks decisions as they evolve during build.

## Planes
- **Control plane:** API server + Postgres + Redis.
- **Execution plane:** worker(s), reaching data only through `internal/coreapi`.

## Authentication & sessions
- **Multi-workspace identity:** Users are accounts belonging to multiple workspaces via `workspace_members` (roles: owner, admin, member).
- **Token model:** Short-lived access tokens (JWT HS256, default 15 minutes) sent as `Authorization: Bearer` and held in memory on the SPA. Long-lived refresh tokens (opaque, default 30 days) stored in an httpOnly `inroad_refresh` cookie (Path=/api/v1/auth), rotated on refresh with reuse detection — replaying a revoked token revokes the entire session family.
- **CSRF protection:** Double-submit token (`csrf_token` cookie + `X-CSRF-Token` header) on cookie endpoints (/auth/refresh, /auth/logout).
- **Authorization:** Deny-by-default — all non-public routes require a valid access token. Public endpoints: POST /register, /login, /refresh, /logout.
