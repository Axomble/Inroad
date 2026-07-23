# Electric Web3 Theme — "Volt" (light-default)

**Date:** 2026-07-24
**Scope:** Re-theme the Inroad SPA to a **white-default, electric yellow-green ("volt") Web3** look, fix
doc⇄code discrepancies uncovered in the process, and keep a recolored dark theme available.

This is a *palette + design-language* change, not a structural one. The dense operator layout, the
system-UI + mono-for-data type identity, and the tactile-button signature are all kept — only relit.

---

## 1. Motivation & the doc⇄code discrepancies this fixes

The current `docs/frontend-design.md` commits to **dark as the primary theme** with a blurple accent and
amber reserved for warmup. The ask is to invert that: light/white default, electric lime accent, Web3
feel. While auditing to do it cleanly, six discrepancies surfaced between the doc and the code:

| # | Discrepancy | Resolution |
|---|---|---|
| 1 | Doc §1 says a light theme "is fully supported," but **no `.light` class is ever applied** — the `ui` slice only tracks `sidebarOpen`, nothing toggles the theme. Light mode is unreachable dead code. | `:root` becomes **light** (default, no class); dark moves to `:root.dark` (opt-in). Flip `@custom-variant dark`. |
| 2 | Doc §2 token table names the secondary-text token `--muted`, but the code token is `--muted-foreground`; `--muted` is actually a *background* alias of `--surface-2`. | Correct the doc table naming. |
| 3 | `button.tsx` destructive variant hardcodes `#a5323c` for the edge — violates the doc's "never hardcode a hex" rule. | Add a `--danger-edge` token; variant references `var(--danger-edge)`. |
| 4 | `auth-showcase.tsx` hardcodes `VIOLET/AMBER/INK` + a `#171022` canvas gradient — stale literals that won't follow the palette. | Repoint the canvas constants to the new lime / orange / paper values. The canvas-perf exception stays, values updated. |
| 5 | `--warm` and `--warn` are the **identical** `#f5a524`, yet the doc calls warm "the one signal that's ours." Sharing a hue erases the distinction. | Move `--warm` to electric **orange** `#ff7a1a` (heat); `--warn` stays amber. |
| 6 | `.tactile` shadows are dark-only (`rgba(0,0,0,.3)` ambient, white top highlight), so the signature reads muddy on white — but the doc claims it "works in light and dark." | Parametrize the tactile shadow/highlight via `--tactile-hi` / `--tactile-ambient*`; light-tune them. |

## 2. Palette — "Volt"

**Accent (shared by both themes).** Electric lime carries fills, active states, and the signature glow.
It never carries text on light (luminance ~0.9 → sub-2:1 contrast); all text is ink, and buttons use
black-on-lime.

| Token | Value | Role |
|---|---|---|
| `--primary` | `#c3f53c` | electric lime — primary fills, active nav |
| `--primary-foreground` | `#12160b` | ink (black-on-lime) |
| `--primary-top` / `-bot` / `-edge` | `#d4f962` / `#b2e81e` / `#6e9200` | tactile gradient top/bottom + deep-lime lip |
| `--primary-glow` / `-hover` | lime box-shadow halos | **signature** — the "charged" glow |

**Light (`:root`, default).** ground `#f7f9f1` (faint lime-tinted paper) · surface `#ffffff` · rail
`#fbfcf6` · surface-2 `#f0f3e6` · border `#e3e8d6` · border-strong `#cfd6bc` · ink `#12160b` · muted
`#565e48` · faint `#6e745c` · ring `#7fa800` (deep lime, visible on white).

**Dark (`:root.dark`, recolored).** ground `#0b0e0a` · surface `#13160f` · surface-2 `#1a1e14` · border
`#282d1f` · text `#eaf0de` · ring `#c3f53c`. Neutral near-black (whisper of green) so lime pops.

**Warm / semantic.** warm `#ff7a1a` (heat, now distinct from accent) · ok `#17b877` (emerald, distinct
from lime) · warn `#f5a524` (amber) · danger `#e5484d` (+ `--danger-edge #a5323c`).

**Charts.** lime `#b2e81e` → teal `#17b0c4` → indigo `#6366f1` → orange `#ff7a1a` → red `#e5484d`.

## 3. Signature — the "charged" tactile button

Keep the hard-lip / press-in physics from doc §5, relit: lime gradient fill, ink label, deep-lime lip,
and a soft electric-lime **glow** that intensifies on hover. The glow is threaded into the existing
`.tactile` box-shadow list via a `--tactile-glow` variable that defaults to transparent and is set only
by the primary variant — so it appears across rest/hover/active without duplicating the state rules, and
never leaks onto secondary/chip controls. Boldness is spent here and only here; everything else stays
ink text on quiet hairlines. Active sidebar nav may carry the same faint halo.

## 4. Change plan (files)

1. `web/src/styles/globals.css` — rewrite `:root` (light) + `:root.dark`; flip `@custom-variant dark`;
   add `--danger-edge`, `--primary-glow*`, `--tactile-hi`, `--tactile-ambient*`; light-tune `.tactile`;
   thread `--tactile-glow` through the shadow list; `color-scheme` per theme.
2. `web/src/components/ui/button.tsx` — `#a5323c` → `var(--danger-edge)`; primary sets `--tactile-glow`.
3. `web/src/features/auth/auth-showcase.tsx` — repoint canvas constants to lime / orange / paper.
4. `docs/frontend-design.md` — rewrite §1–§2 (light-default Volt); fix the `--muted` naming (disc. #2)
   and the warm/warn separation (disc. #5).

## 5. Non-goals

- No layout, IA, component-architecture, or type-scale changes.
- No theme-toggle UI (the mechanism — `:root.dark` — is in place; wiring a toggle control is future work).
- No changes to backend, API, or non-`web/` code.

## 6. Verification

- `npm run build` (or `vite build`) succeeds; existing Vitest suite still green.
- Drive the app in a browser: app renders **light by default**, primary button shows the lime charged
  glow, contrast holds (ink ≥ 4.5:1 on white). Spot-check dark via `document.documentElement.classList.add('dark')`.
