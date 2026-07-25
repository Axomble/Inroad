# Sequence Steps — Design

**Date:** 2026-07-26
**Branch:** `feature/sequence-steps` (off `main`, migration head `000014`)
**Status:** Design — approved (both open decisions resolved by product)

## 1. Goal & the bug it fixes

Give campaigns a real multi-step sequence editor — the platform's headline "sequencing"
capability, currently 100% absent from the UI (backend step CRUD exists but no component
calls it).

**Critical bug this fixes:** `CreateCampaign` stores `subject`/`body_text` on the campaign
row but inserts **zero** `sequence_steps`. The sender reads exclusively from steps
(`stepsendjob.go`), and `Launch` rejects a campaign with 0 steps (`ErrNoSteps`). So today
**every campaign created through the UI is unlaunchable**, and the subject/body the create
form collects are orphaned (never sent). This feature makes the create→launch path work.

## 2. Decisions (resolved with product)

1. **Auto-seed step 1 on campaign create.** `CreateCampaign` becomes transactional: insert
   the campaign AND its step 1 (`step_order=1`, `delay_seconds=0`, subject/body/body_html
   from the request) in one DB transaction. A new campaign is immediately launchable; the
   editor manages step 1 (editable) + follow-ups; follow-up reply-threading already reads
   step-1's subject as the thread subject (`stepsendjob.threadSubject`).
2. **Full reorder** — new backend endpoint + drag-and-drop UI (see §4, §5).

## 3. Backend — auto-seed step 1

- `internal/platform/db/queries/campaign.sql`: wrap create in the store as a transaction
  (or add a `SeedFirstStep` insert run in the same tx as `CreateCampaign`). The step row:
  `(workspace_id, campaign_id, step_order=1, delay_seconds=0, subject, body_text, body_html)`.
- `campaign.Service.Create` orchestrates: create campaign → seed step 1 → return. Both
  writes share one tx so a failure leaves no half-created campaign. Workspace-pinned.
- `campaign.subject` stays as the list-view label + create-time seed of step 1; after
  creation the two may diverge (editing step 1 does not rewrite `campaign.subject`) — the
  sequence sender + threading use the **step**, so this is cosmetic only. Documented.
- Backward-compat: existing campaigns created before this change keep 0 steps; the editor's
  empty state + "Add step" cover them. Tests that create a campaign now also assert step 1.

## 4. Backend — reorder endpoint

`POST /api/v1/campaigns/{id}/steps/reorder`

- **Body:** `{ "step_ids": ["<uuid>", ...] }` — the FULL ordered list of the campaign's step
  ids, in the desired order.
- **Response 200:** `SequenceStep[]` (the steps in their new order) — lets the client refresh
  cache from the authoritative result.
- **Semantics:** validate `step_ids` is exactly a permutation of the campaign's current step
  ids (same set, no dupes, no extras) → **400** otherwise. Rewrite `step_order` to `1..N` in
  one transaction. **Draft-only** (structural edit) → **409** if the campaign isn't draft,
  mirroring create/delete. **404** if campaign/step not found. Workspace-pinned throughout;
  a step id from another campaign/tenant → 400/404, never reordered.
- Lives in the `sequencestep` domain (`Service.Reorder`, `Store.Reorder`), mirroring the
  existing create/delete draft-guard (`requireDraft`) and `assertStepInCampaign` isolation.
- OpenAPI: add the path + a `ReorderStepsRequest` schema; document 200/400/401/404/409.

## 5. Frontend — sequence editor

A **"Sequence"** section at the TOP of `CampaignDetail` (above Sends stats — the sequence is
the campaign's definition). Uses the generated `useListStepsQuery` + create/update/delete
hooks + the injected `reorderSteps` (contract-first, reconciled after backend `gen:api`).

- **Step card** (ordered): "Step N" · delay label (`0`→"Immediately", else humanized
  "3 days after previous") · subject · 1-line body preview · Edit / Delete (draft-only).
- **Add step** (draft-only): inline form — delay (days + hours → seconds), subject,
  body_text. Appends via `createStep`.
- **Edit** (any status — content is live-reference): same form, `updateStep`. Delay edit
  allowed.
- **Delete** (draft-only): confirm dialog, `deleteStep`.
- **Reorder** (draft-only): drag-and-drop via **@dnd-kit** (accessible: pointer + keyboard,
  `SortableContext`), optimistic order, commit via `reorderSteps({ id, step_ids })`; on
  error, refetch to revert. Non-draft: drag disabled.
- **Status-awareness:** draft → full editor (add/edit/delete/reorder). launched/paused/done →
  cards render read-mostly with **content Edit** enabled; Add/Delete/Reorder disabled with a
  tooltip ("Structural changes are draft-only"). Backend 409 is the backstop; the UI just
  hides the affordances and surfaces the 409 via `httpStatus` if it slips through.
- **States:** loading skeleton, empty ("No steps yet"), typed error via `@/lib/rtk-error`
  (no ad-hoc casts). Colour is never the only signal; icon-only controls get `aria-label`.
- **Cache:** `listSteps` provides an `Step`/`Enrollment`-style tag; create/update/delete/
  reorder invalidate it so the list refreshes. Route stays code-split (dnd rides the
  campaigns chunk).

## 6. Security / invariants

- Every step query (seed, reorder, CRUD) workspace-pinned from the JWT; a cross-tenant
  campaign/step id → 404/400 before any write (mirror `assertStepInCampaign`).
- Auto-seed + reorder are transactional (no half-writes).
- Structural edits (add/delete/reorder) draft-only; content edits live-reference — enforced
  server-side, reflected client-side.
- No secret handling, no new outbound calls.

## 7. Testing

- **Backend unit:** seed-step-1 on create (campaign now has exactly one step, order 1,
  delay 0, mirroring subject/body); reorder happy path (permutation → 1..N); reorder
  rejects non-permutation (400), non-draft (409), cross-tenant/foreign step (400/404).
- **Backend integration (Postgres):** create→step-1 exists→launch succeeds (the bug fix,
  end-to-end); reorder persists new order; reorder is atomic + workspace-scoped.
- **Frontend (vitest):** step list renders ordered with delay labels; add/edit/delete flows
  (draft) call the right mutations; reorder calls `reorderSteps` with the new id order;
  status-awareness (launched hides add/delete/reorder, keeps edit); loading/empty/error.

## 8. Delivery order (contract-first, parallelizable)

1. **Backend:** auto-seed step 1 (tx) + reorder endpoint + OpenAPI + `gen:api`; unit +
   integration tests. (`backend-developer`)
2. **Frontend (parallel):** sequence editor + @dnd-kit reorder against the agreed contract
   via `injectEndpoints`; vitest. (`frontend-developer`)
3. **Reconcile** injected vs generated `reorderSteps`; gate (security + reviewer +
   performance + qa); commit; push.
