# Reply Classification (Design)

**Date:** 2026-07-25
**Branch:** `feature/reply-classification` (off `main`, migration head `000013`)
**Status:** Design — pending review

## 1. Goal

Classify each inbound reply that matches one of our sends into a small, stable set
of classes and act on it — fixing two real bugs in today's binary reply handling and
unlocking sentiment-tagged replies for future reply-branching / CRM views.

Inspired by Warmbly's `replyclassify` (Apache-2.0, same Go stack) but **reimplemented
better**: it fixes a correctness bug in Warmbly's lexicon, removes global mutable
state, adds boundary-aware matching, and ships with Inroad's higher test density.
Attribution to Warmbly (Apache-2.0) is preserved in the package doc + a `NOTICE` entry.

Non-goals: the AI/LLM Layer-3 classifier is a **seam only, unwired** (Inroad has no AI
provider) — the ambiguous middle resolves to `unknown` with zero network calls, exactly
as the competitive analysis flagged ("the LLM layer is optional").

## 2. What's wrong today (the value)

`internal/worker/inbox/poll.go` `processMessage`: an inbound reply that matches a send
is passed through `IsAutoReply(header)` (which checks **only** `Auto-Submitted`), and if
not auto, `MarkReplied` stops the enrollment. Two real gaps:

1. **OOO trap:** an "Out of Office" auto-reply *without* an `Auto-Submitted` header
   **wrongly stops the sequence** — the contact never gets the rest of the campaign.
2. **No reply-based unsubscribe:** a reply saying "remove me / unsubscribe" just stops
   the one enrollment; the address is **not suppressed** (compliance gap — we only
   suppress on hard bounce + the `/u` link).

## 3. Classes + sources

Classes (stored on the enrollment): `positive` · `negative` · `neutral` · `auto_reply`
· `out_of_office` · `unsubscribe` · `unknown`.
Source (which layer decided): `header` · `lexicon` · `model` · `""` (unclassified).
Confidence: `[0,1]`, stored for future thresholding / UI.

## 4. Layered classifier — `internal/platform/replyclassify` (pure, reusable)

```go
type Input  struct { Headers map[string][]string; Subject, BodyText string }
type Result struct { Class, Source string; Confidence float64 }

// ModelClassifier is the OPTIONAL Layer-3 seam (nil = disabled). No global state.
type ModelClassifier interface {
    Classify(ctx context.Context, subject, body string) (class string, ok bool)
}
type Classifier struct { model ModelClassifier } // model may be nil
func New(model ModelClassifier) *Classifier
func (c *Classifier) Classify(ctx context.Context, in Input) Result
```

- **Layer 1 — headers (deterministic, offline):** OOO subjects ("out of office",
  "automatic reply", "auto:", "on vacation"…), RFC 3834 `Auto-Submitted`
  (`auto-replied`→OOO, else `auto_reply`), vendor headers (`X-Autoreply`,
  `X-Auto-Response-Suppress`), `Precedence: bulk/junk/list/auto_reply`,
  `multipart/report; …delivery-status` (DSN → `auto_reply`), null `Return-Path`,
  mailer-daemon/no-reply senders. **Improvement over Warmbly:** also honor
  `List-Unsubscribe`/`Feedback-ID`/`X-Auto-Response-Suppress` as automated signals.
- **Layer 2 — lexicon (deterministic, offline):** compliance words
  (`unsubscribe`/`remove me`/`stop emailing`…) win first (compliance-safe), then
  rejection → `negative`, then interest → `positive`.
  **Improvements over Warmbly (correctness):**
  - **Fix the "not interested" bug** — Warmbly checks positive *before* negative and
    `"interested"` is a positive keyword, so **"not interested" mis-classifies as
    positive**. We check **negative before positive** AND make positive matches
    negation-aware (a positive keyword preceded by `not/isn't/won't/no` doesn't fire).
  - **Boundary-aware matching** for short tokens (`stop`, `no need`) so `"nonstop"` /
    `"stopped by"` don't false-positive; phrase keywords still use contains.
- **Layer 3 — model (optional):** only the ambiguous middle reaches it; constrained to
  `positive|negative|neutral`. `model == nil` → `unknown`, no I/O. Unwired in Inroad
  now; the seam + an injected (not global) classifier keep it ready.

**Improvement — no global mutable state:** Warmbly wires Layer 3 via a package global
(`modelClassify` + `sync.RWMutex` + `SetModelClassifier`). We inject the (optional)
model into a `Classifier` struct — pure DI, concurrent-safe by construction, trivially
testable, no hidden global.

## 5. Integration — `processMessage` (inbox poll)

Replace the binary `IsAutoReply`→`MarkReplied` branch. For a reply that matches a send
(`FindSendByMessageID`):

```
r := classifier.Classify(ctx, Input{Headers, Subject, BodyText})
switch {
case r.Class == auto_reply || out_of_office:   // OOO-trap fix: do NOT stop
    record class on the enrollment; keep sending; (count as "skipped", not a reply)
case r.Class == unsubscribe:                    // compliance: suppress + stop
    core.MarkUnsubscribed(ctx, enrollmentID, workspaceID, contactEmail)
default:                                        // positive/negative/neutral/unknown
    core.MarkReplied(ctx, enrollmentID, workspaceID, r.Class, r.Source)  // stop, tagged
}
```

The DSN/hard-bounce path (`ParseDSN` → `MarkBounced`) is unchanged and still runs first;
classification handles the non-bounce reply branch. `IsAutoReply` is subsumed by Layer 1
(kept or removed as the impl sees fit; Layer 1 is a strict superset).

## 6. coreapi + data model

- Migration `000014`: add `reply_class text`, `reply_source text`, `reply_confidence
  real`, `replied_at timestamptz` (nullable) to `sequence_enrollments` (where stop
  state already lives). A CHECK constraint pins `reply_class` to the 7 classes.
- `coreapi.Client`:
  - `MarkReplied(ctx, enrollmentID, workspaceID, replyClass, replySource string) error`
    — extend to carry + store the class/source/confidence (or add a sibling; keep one
    method, threading the class). Stops the enrollment (reason `replied`) as today, now
    tagged.
  - `MarkUnsubscribed(ctx, enrollmentID, workspaceID, email string) error` (new) —
    suppress the address (reuse the suppression insert `MarkBounced` uses) **and** stop
    the enrollment (reason `unsubscribed`). Workspace-pinned.
  - Automated (`auto_reply`/`out_of_office`) replies call **neither** — a new
    `RecordReplyClass(ctx, enrollmentID, workspaceID, class, source, confidence)` stores
    the class **without** stopping (so the OOO contact keeps receiving the sequence).
- `enrollment.StopReason` gains `unsubscribed` (alongside replied/bounced/…).

## 7. Frontend

Surface the reply class on the campaign/contact view (a small pill: Positive / Negative
/ OOO / Unsubscribed / …). Reuses the existing campaign detail surface. React best
practices (typed RTK, code-split, one badge component). The reply-branching editor is a
future phase; this ships the signal + display.

## 8. Security / compliance invariants

- `unsubscribe`-classified replies **suppress the address** (compliance-safe default);
  suppression is workspace-scoped (reuse the existing suppression path).
- Classification is pure/deterministic (Layers 1–2) and side-effect-free; re-processing a
  message is idempotent. No secret handling, no new outbound calls (Layer 3 unwired).
- Automated replies never stop a sequence (OOO trap closed) — a correctness+deliverability
  fix, not a security one, but it prevents silent campaign truncation.

## 9. Testing (our edge)

- **Unit (pure, table-driven):** each class from representative headers/subjects/bodies;
  **the "not interested" → negative regression** (the Warmbly bug); boundary-matching
  (`nonstop` ∉ unsubscribe); compliance-first ordering; OOO-by-subject without
  Auto-Submitted; DSN content-type → auto_reply; Layer-3 nil → unknown (no I/O); a fake
  ModelClassifier resolves the middle.
- **Integration (Postgres):** an OOO reply does NOT stop the enrollment (still active,
  reply_class stored); an "unsubscribe" reply suppresses + stops; a "not interested"
  reply stops tagged `negative`; the reply_class round-trips.
- **Backward-compat:** existing reply/bounce inbox tests stay green (the DSN path + the
  match→stop behavior for plain human replies is preserved, now tagged).

## 10. Delivery order (independently testable)

1. `internal/platform/replyclassify` — Layers 1–2 (+ Layer-3 seam), the improvements
   above, full unit tests, Apache-2.0 attribution. (Pure; no DB.)
2. Migration `000014` (`reply_class`/`reply_source`/`reply_confidence`/`replied_at` +
   CHECK) + `enrollment` store/state for the new columns + `unsubscribed` StopReason.
3. coreapi: extend `MarkReplied`, add `MarkUnsubscribed` + `RecordReplyClass`; inprocess
   impls (workspace-pinned; suppression reuse).
4. Integrate the classifier into `processMessage` (OOO-trap fix + unsubscribe suppression
   + tagged stop); wire the `Classifier` (Layer-3 nil) into the inbox worker; tests.
5. Frontend: reply-class pill on the campaign/contact view.
6. Docs (`architecture.md` reply pipeline, `security.md` compliance note) + `NOTICE` attribution.

## 11. References

- Warmbly `internal/app/replyclassify/{classifier,headers,lexicon,model}.go` (Apache-2.0
  — inspiration + attribution; improvements documented above).
- This repo: existing reply/bounce detection `internal/worker/inbox/{poll,reply,dsn}.go`;
  competitive analysis 03 (reply classification = P1 Do-better).
