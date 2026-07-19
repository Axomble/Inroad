# Inroad Architecture

See `docs/superpowers/specs/2026-07-19-outpost-repo-architecture-design.md` for the
full layout rationale. This document tracks decisions as they evolve during build.

## Planes
- **Control plane:** API server + Postgres + Redis.
- **Execution plane:** worker(s), reaching data only through `internal/coreapi`.
