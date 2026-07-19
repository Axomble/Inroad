# 0005 — Apache 2.0 license

**Status:** Accepted

## Context
Inroad ships open-core: a free self-hostable Core and a future commercial Cloud.
The license choice (per PRD §14.3) affects whether competitors can re-host Core
commercially. Apache 2.0 maximizes adoption and trust and includes an explicit
patent grant; AGPL-3.0 would deter competitors re-hosting but adds friction for
some corporate adopters.

## Decision
License Core under **Apache 2.0** (`LICENSE` + `NOTICE`, `Copyright 2026 Ahmed
Mustufa Malik`).

## Consequences
- Maximum adoption and minimal legal friction for self-hosters and integrators.
- No copyleft protection against a competitor hosting Core commercially; the moat
  is the Cloud offering (shared warmup pool, ops, billing) and the vendor-trust
  wedge, not the license.
- Revisit only if a competitor materially re-hosts Core; a relicense would not be
  retroactive.
