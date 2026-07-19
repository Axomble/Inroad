# Inroad Frontend Design System

The visual and interaction foundation for the Inroad web app (`web/`). This document is the source of
truth for how the UI looks, behaves, and is composed. Feature work should read this before adding screens.

> **Product framing.** Inroad is an outbound-operations console: cold-email sequencing plus mailbox
> warmup. The people using it run it all day, watch numbers move, and act on what needs attention. The UI
> is *scanned and operated*, not read top-to-bottom. That framing drives every choice below — density over
> whitespace, state encoded in form as well as number, and a shell that feels alive rather than static.

---

## 1. Design language

**A dark, data-dense operator console.** Deep purple-black surfaces, hairline dividers instead of cards
with gutters, tiny tracked labels, and monospace for every number and identifier. Structure comes from
rules and alignment, not shadows and rounded boxes.

Three principles:

1. **One accent, spent deliberately.** A single "blurple" marks *the* primary action on a screen. A
   second warm hue (amber) is reserved for exactly one concept — **mailbox warmup / "heat"** — because
   that is the act this product is named for. Everything else is neutral. Color that means something
   stays meaningful.
2. **Tactile controls.** Buttons stand slightly proud of the surface, lift to meet the cursor, and press
   *into* the page. Interactive things look interactive and feel physical. (See §5.)
3. **Quietly alive.** Live counts, status dots, and small motion motifs make the shell read as a running
   operation. Motion is subtle, meaningful, and always gated behind `prefers-reduced-motion`.

We deliberately commit to **dark as the primary theme**; a light theme is fully supported through tokens
but dark is the default the product is designed in.

---

## 2. Color tokens

All color lives as CSS custom properties in `web/src/styles/globals.css` and is exposed to Tailwind v4
through `@theme inline`. **Never hardcode a hex or a raw `slate-*`/`violet-*` utility in a feature
screen** — consume the semantic token (`bg-surface`, `text-muted`, `border-border`, `text-primary`).
That single rule is what keeps the app re-themable.

### Semantic roles

| Token | Dark value | Role |
|---|---|---|
| `--background` | `#14101c` | App ground (deepest) |
| `--rail` | `#171320` | Sidebar / chrome |
| `--surface` | `#1c1726` | Raised panels, header, toolbars |
| `--surface-2` | `#241d31` | Inputs, chips, nested surfaces |
| `--border` | `#2e2740` | Hairline dividers |
| `--border-strong` | `#3a3350` | Control borders |
| `--foreground` | `#edeaf4` | Primary text |
| `--muted` | `#9a93ad` | Secondary text |
| `--faint` | `#6f6885` | Labels, IDs, disabled |
| `--primary` | `#7c5cff` | Accent — primary actions, active nav |
| `--warm` | `#f5a524` | Reserved: warmup / heat only |
| `--ok` | `#34d399` | Healthy, sending, live |
| `--warn` | `#f5a524` | Warning, paused |
| `--danger` | `#fb6d77` | Error, failing, destructive |

### Chart hues

`--chart-1..5` = violet, cyan, emerald, amber, rose. Ordered so the first two carry the most weight and
stay distinguishable for the most common colorblindness types.

### Rules

- **Accent discipline:** one `primary` button per screen region. If two things look primary, one is wrong.
- **Warm is sacred:** `--warm` appears only on warmup surfaces (warmup toggles, the "Warming" stat, the
  ramp indicator). Using it as a generic highlight dilutes the one signal that's ours.
- **Semantic ≠ accent:** good/warn/critical are their own scale and never stand in for the accent.
- **Contrast floor:** body text ≥ 4.5:1, large text ≥ 3:1, in both themes. Verify when adding tokens.

---

## 3. Typography

The Artifact/Tailwind CSP and self-hosting posture mean we don't depend on a webfont CDN at runtime. The
identity is carried by a **system UI stack for prose and a monospace for data** — which is also honest to
the developer/deliverability audience.

| Role | Stack | Used for |
|---|---|---|
| `font-sans` | `system-ui, -apple-system, "Segoe UI", Roboto, …` | UI text, headings, body |
| `font-mono` | `ui-monospace, "Cascadia Code", "JetBrains Mono", Menlo, …` | Numbers, IDs, labels, eyebrows |

Type scale (dense by design):

- **Eyebrow / label:** `font-mono`, `10–11px`, `uppercase`, `tracking-[0.14em]`, `text-faint`.
- **Row / body:** `13.5px`.
- **Secondary / caption:** `12.5px`, `text-muted`.
- **Stat value:** `26–27px`, `font-light`, `tabular-nums`.
- **Page title:** `text-xl`/`text-2xl`, `tracking-tight`, `text-wrap: balance`.

**Every digit that lines up in a column uses `tabular-nums`.** Every ID/hash uses `font-mono`. These two
habits do most of the work of making the app look engineered rather than generic.

---

## 4. Layout & spacing

**App shell** (`components/layout/app-shell.tsx`):

```
┌──────────────────────────────────────────────────────────┐
│  AppHeader  (h-14) — logo · org · breadcrumb · ⌘K · user  │
├───────────┬──────────────────────────────────────────────┤
│           │  <main> — the white/dark work surface,        │
│ AppSidebar│  rounded top-left where it meets the chrome,   │
│  (w-64)   │  scrolls independently                        │
│           │                                               │
└───────────┴──────────────────────────────────────────────┘
```

- Sidebar is `w-64`, grouped nav with tracked-uppercase section labels and hairline separators. Live
  counts sit right-aligned per row. Below `md` it becomes an off-canvas drawer over a scrim; state lives
  in the `ui` redux slice (`sidebarOpen`). No icon-rail collapse — full width or drawer.
- Content surface has a single rounded top-left corner (`md:rounded-tl-2xl`) and a hairline top/left
  border, so chrome and content read as one continuous frame.

**Page scaffold** (`components/layout/page.tsx`) — used instead of ad-hoc Card layouts so every screen has
the same rhythm:

- `Page` — fills the surface, no max-width.
- `PageTopbar` — `h-12`, uppercase eyebrow + optional subtitle on the left, actions on the right, sticky.
- `SectionBar` — slimmer sub-toolbar (search, filters, a mono count).
- `StatStrip` / `Stat` — full-width KPI row divided by **vertical hairlines**, not a grid of cards. Stat
  value is `font-light tabular-nums`; label is a mono eyebrow; optional status dot.
- `PageBody` — the scroll region.
- `EmptyBlock` — tight, text-led empty state with a single CTA. Empty is an invitation to act, never a
  blank gap.

**Spacing:** lay out sibling groups with flex/grid + `gap`, never per-element margins. Wide content
(tables, flow canvas) scrolls inside its own `overflow-x-auto` container so the page never scrolls
sideways.

**Z-index scale:** `10` dropdowns · `20` sticky chrome · `30` drawer · `40` scrim · `50` modal/toast.

---

## 5. The tactile button (signature interaction)

Depth is built from a **hard, non-blurred bottom edge** (the "lip") plus an **inset top highlight** — not
a soft drop shadow. Three states:

1. **Rest** — stands taller: `box-shadow: inset 0 1px 0 rgba(255,255,255,.1), 0 2px 0 <edge>, 0 5px 12px rgba(0,0,0,.3)`.
2. **Hover** — lifts `translateY(-1.5px)` and brightens, to greet the cursor.
3. **Active** — recesses: lip collapses to `0`, inset shadow appears, drops `translateY(2px)` in ~34ms so
   the press feels instant.

Timing is asymmetric on purpose: springy ~130ms return, snappy ~34ms press. All of this is encoded in the
`Button` variants and a `.btn-tactile` utility layer in `globals.css`, and is disabled under
`prefers-reduced-motion`. Filter chips use the same physics with a shallower lip.

Two button systems, both tactile:

- **`Button`** (`variant="primary" | "warm" | "secondary" | "outline" | "ghost" | "destructive"`) — the
  full control, for CTAs, dialogs, forms.
- **Chips** (`variant="chip"` + `size="chip"`) — toolbar filters/sort/scope selectors.

---

## 6. Component architecture

**shadcn/ui (new-york) + Radix + cva, adapted for React 19.**

- **Primitives** live in `web/src/components/ui/` (kebab-case files: `button.tsx`, `dropdown-menu.tsx`).
  They are unstyled-behavior-from-Radix + our tokens via cva. They own no domain knowledge.
- **Layout** lives in `web/src/components/layout/` (shell, page scaffold).
- **Shared** app widgets (status pills, animated counts) live in `web/src/components/shared/`.
- **Feature components** live in `web/src/features/<domain>/` and mirror backend domains. Features never
  import each other; shared UI comes from `components/*`. This is the frontend echo of the backend rule
  that `app/*` domains don't import each other.

### React 19 conventions (enforced by review)

1. **`ref` is a prop.** No `forwardRef`. Function components take `ref` directly:
   `function Button({ className, ref, ...props }: Props & { ref?: React.Ref<HTMLButtonElement> })`.
2. **`data-slot` on every primitive root** (shadcn new-york convention) — enables styling and testing
   hooks without extra classes.
3. **Variants via `cva`**, exported as a named constant (e.g. `buttonVariants`) so other components can
   compose the same styles. This is the shadcn convention; oxlint emits a benign
   `only-export-components` fast-refresh *warning* for co-locating the variant with its component —
   accepted, since it affects HMR only, not correctness.
4. **`cn()`** (`clsx` + `tailwind-merge`) is the only way class strings are combined, so consumer
   overrides win predictably.
5. **Don't reach for `useMemo`/`useCallback` reflexively.** Memoize only measured hot paths; prefer
   deriving during render. (React Compiler-friendly.)
6. **Prefer Actions for forms** — `useActionState` / `<form action>` and `useFormStatus` for pending
   state over hand-rolled loading booleans, where the form does real async work.
7. **Data fetching is RTK Query only**, generated from `api/openapi.yaml` into `store/api.ts` (never
   hand-edited). Components consume generated hooks. UI-only state goes in redux slices that are the
   *only* things redux-persist whitelists — never the `api` reducer.

### Adding a component — checklist

- [ ] File is kebab-case in the right folder (`ui` / `layout` / `shared` / `features/<domain>`).
- [ ] Colors/spacing use tokens, not raw hex or `slate-*`.
- [ ] `ref` as prop, `data-slot` on root, `cn()` for classes.
- [ ] Interactive elements: `cursor-pointer`, visible `focus-visible` ring, ≥ min touch target.
- [ ] Motion respects `prefers-reduced-motion`.
- [ ] Works in light and dark (test the toggle).
- [ ] Icon-only controls have `aria-label`; images have `alt`; inputs have a `<label>`.

---

## 7. Information architecture

Sidebar groups (labels are mono eyebrows):

- **(top)** — Inbox (unified inbox, unread badge)
- **Email** — Mailboxes, Campaigns, Contacts, Analytics, Deliverability
- **CRM** — Pipelines, Deals, Tasks, Meetings
- **Resources** — Templates, Integrations, Automations, API Keys, Audit log
- **(pinned)** — Settings, then the user menu

Each data row may carry a live count fed by RTK Query and invalidated over the realtime channel. Counts
tween rather than snap. Warmup-related rows carry the amber motif; attention-needed rows a small pulse.

---

## 8. Accessibility & performance floor (non-negotiable)

- Contrast ≥ 4.5:1 body / 3:1 large, both themes.
- Visible keyboard focus everywhere; tab order matches visual order.
- Touch targets ≥ 44×44 for pointer-coarse; icon buttons get `aria-label`.
- `prefers-reduced-motion` disables transforms and ambient motion.
- Reserve space for async content (skeletons) so layout never jumps.
- Charts/data always occupy their footprint — empty renders a framed baseline with a caption, not a gap.
- Use `transform`/`opacity` for motion, 150–300ms for micro-interactions.

---

## 9. File map

```
web/src/
  styles/globals.css            # tokens (@theme inline), tactile utilities, keyframes
  lib/utils.ts                  # cn()
  components/
    ui/                         # button, card, badge, input, label, separator,
                                #   avatar, dropdown-menu, tooltip, skeleton, …
    layout/                     # app-shell, app-sidebar, app-header, page
    shared/                     # status-pill, …
  features/<domain>/            # auth, campaigns, mailboxes, unibox, …
  store/                        # api.ts (generated), slices/ui.ts, index.ts
```
