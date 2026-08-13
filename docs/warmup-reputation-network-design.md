# Inroad Reputation Network

**Status:** Phase 0 implemented; Phases 1-4 proposed
**Scope:** Warmup pool safety, reputation measurement, partner allocation, recovery, and campaign gating
**Supersedes:** None. This extends ADR 0006; the workspace-local pool remains the self-hosted default.

## 1. Outcome

Inroad should not copy a larger shared pool with a longer list of health states. It
should build a reputation control plane that can answer four separate questions:

1. Is this mailbox safe to send warmup traffic?
2. Is this mailbox trustworthy as an observer of someone else's placement?
3. Which provider, domain, relay, or infrastructure identity is actually failing?
4. What evidence is sufficient to increase volume or return a mailbox to the
   healthy network?

The design protects healthy participants by default, treats missing evidence as
unknown rather than healthy, and makes every automatic action explainable from an
immutable observation trail.

## 2. Why the current engine stops short

The existing engine has strong delivery mechanics:

- real threaded sends and recipient-side engagement;
- signed receipt tokens and warmup-message isolation;
- sender-attributed inbox/spam observations;
- deterministic pair spreading;
- claim-before-send idempotency;
- gradual ramping and health-scaled campaign caps;
- mailbox-to-worker affinity.

Its safety model is nevertheless v1-level:

- `healthy`, `watch`, `throttled`, and `paused` are overloaded as both health and
  pool eligibility;
- only the seven-day spam-placement ratio is live in warmup health evaluation;
  bounce and invalid-token inputs are currently passed as zero;
- zero observed deliveries produce a zero spam rate and therefore look healthy;
- recovery is elapsed-time plus cleaner windows, not requalification on a
  statistically meaningful probation sample;
- partner selection is greedy and mailbox-local, with no pool lane, domain,
  provider-route, observer-trust, or correlated-failure constraints;
- daily rollups erase evidence needed for confidence, trend, and root-cause
  analysis;
- the local pool deliberately has no safe cross-tenant coordinator or admission
  control.

These were reasonable trade-offs for shipping a secure, self-hostable first
engine. They are not enough for operating a reputation network.

## 3. Core model: three independent axes

A single `health_state` must not decide everything. The control plane maintains
three independent assessments for each participant.

### 3.1 Sender reputation

How the participant's outbound mail performs:

- spam and tab placement by destination provider;
- hard-bounce and complaint rates;
- delivery latency and deferral patterns;
- authentication posture;
- volume/ramp anomalies;
- missing or invalid signed receipts.

### 3.2 Observer trust

How much weight to give placement reported by the participant:

- token verification and replay history;
- agreement with controlled sentinel observations;
- receipt completeness and timing plausibility;
- provider/API integrity;
- correlated or impossible reporting patterns;
- workspace/node admission tier.

A mailbox can have poor sender reputation but remain a reliable observer. It can
also send successfully while being an untrusted observer. The two scores must not
contaminate one another.

### 3.3 Infrastructure and identity risk

Reputation is attached to more than a mailbox. Inroad builds a fault-domain graph:

```text
mailbox
  -> organizational domain
  -> DKIM signing domain
  -> return-path domain
  -> sending provider / relay account
  -> observed outbound MTA IP and ASN
  -> Inroad worker egress identity
  -> destination provider route
```

Worker egress IP is an operational identity, but it is not automatically the IP
that the destination mailbox provider scores. Gmail, Microsoft, and many SMTP
relays deliver through their own MTAs. Inroad should extract the observed sending
route from authenticated results and `Received` headers instead of assuming the
worker's connection IP is the delivery IP.

## 4. Pool lanes and blast-radius policy

Pool membership becomes an explicit lane, separate from health scoring.

| Lane | Can send to | Can receive from | Purpose |
|---|---|---|---|
| `sentinel` | Any policy-approved lane | Any lane within strict exposure budgets | Controlled, high-trust measurement |
| `healthy` | Healthy peers and sentinels | Healthy peers only | Normal reputation traffic |
| `watch` | Sentinels and watch peers | Watch peers only | Diagnose early degradation without exposing healthy peers |
| `probation` | Sentinels and probation peers | Probation peers only | New-member admission |
| `recovery` | Sentinels and recovery peers | Recovery peers only | Requalification after quarantine |
| `quarantine` | Nobody | Nobody | Cooldown and investigation |
| `blocked` | Nobody | Nobody | Manual review required |

Healthy customer mailboxes never receive traffic from unknown, watch, recovery,
quarantined, or blocked members. Sentinels absorb bounded diagnostic exposure and
are split into cells, capped, monitored, rotated, and retired so they cannot become
a recognizable high-volume seed network.

For self-hosted installations without sentinels, the local coordinator can use
same-lane peers, but the UI must label the resulting confidence as peer-only and
must not silently promote a participant on insufficient evidence.

## 5. Lifecycle state machine

Lifecycle controls admission and recovery; it does not replace the three risk
axes.

```text
pending_auth
     |
     v
  probation ----failed evidence----> quarantine
     |                                  |
     | qualified                        | cooldown + prerequisites
     v                                  v
   healthy <----qualified----------- recovery
     |  ^                               |
     |  | clean evidence                | failed evidence
     v  |                               v
    watch ------------------------> quarantine
     |
     | severe signal
     v
   blocked --manual approval------> recovery
```

Default policy:

- `pending_auth`: SPF, DKIM, DMARC, provider connectivity, and mailbox ownership
  must pass before traffic starts.
- `probation`: starts at 5 messages/day. Promotion requires observations from
  multiple independent destinations and a confidence-qualified clean window.
- `watch`: volume and pacing are reduced immediately; the mailbox leaves the
  healthy lane while diagnosis continues.
- `quarantine`: no warmup or new campaign leads. A cooldown is necessary but not
  sufficient for exit.
- `recovery`: starts at 5 messages/day in the recovery lane. It must earn promotion
  through fresh evidence; elapsed time alone never reintegrates it.
- `blocked`: campaigns and warmup stop. Re-entry requires operator approval and
  then recovery; catastrophic abuse can permanently retire the participant.

In-flight human replies may use a separately configured policy from new campaign
leads. This avoids abandoning a real conversation while still stopping reputation
expansion.

## 6. Evidence and confidence

### 6.1 Immutable observations

Every delivery attempt and receipt produces immutable events before aggregation:

- sender and recipient participant IDs;
- mailbox/domain/provider/relay/route fault-domain IDs;
- pool lane and pair-lease ID;
- normalized placement: `primary`, `tabbed`, `spam`, `quarantine`, `other`, or
  `unknown`;
- provider-native folder/label;
- token result, replay status, send/receive timestamps, and engagement timestamps;
- observed MTA IP/ASN and authentication results where available;
- content/thread fingerprint and generator version;
- observer trust weight at decision time.

Daily stats remain useful as projections, but never become the source of truth.

### 6.2 Confidence-qualified rates

For a binary policy signal such as spam placement, maintain a time-decayed Beta
posterior instead of using only `spam / (inbox + spam)`:

```text
effective spam   = sum(time_decay * observer_trust * spam_observation)
effective inbox  = sum(time_decay * observer_trust * inbox_observation)
posterior        = Beta(1 + effective_spam, 1 + effective_inbox)
```

Store the posterior mean, 95% credible interval, effective sample size, observer
count, destination-provider count, and age of newest evidence. Actions use both
the estimate and uncertainty:

- no sample means `unknown`, never `healthy`;
- escalation can use a fast window when a severe signal has enough evidence;
- recovery uses the slower window and the upper credible bound;
- promotion requires diversity, not twenty reports from one recipient or one
  provider;
- low-trust peer reports contribute less than controlled sentinel reports.

Complaint, bounce, token-abuse, and authentication signals retain their own
thresholds. A display score may summarize health for humans, but automation acts
on named metrics so every decision remains explainable.

### 6.3 Trend and change detection

Keep two views:

- a fast EWMA/change detector for sudden degradation;
- a slower 7- to 30-day confidence model for promotion and recovery.

This gives fast containment without allowing a few good messages to erase a long
bad history.

## 7. Partner allocation

Partner selection becomes a batch allocator, not `ORDER BY least_recent LIMIT 1`.
Every five minutes, the coordinator turns due send slots into pair leases using a
min-cost matching or constrained greedy fallback.

### 7.1 Hard constraints

- sender and recipient lanes are compatible;
- both sides are enabled, connected, and inside waking-hour/capacity budgets;
- sender and recipient are different mailboxes and, by default, different domains;
- the exact pair has not interacted within the configured cooldown (default seven
  days), unless continuing a valid thread;
- pair, domain-pair, tenant-pair, and recipient inbound caps are not exceeded;
- blocked fault domains and provider routes are excluded;
- a recipient cannot measure its own workspace in the shared network unless the
  assignment is explicitly local;
- one observer, provider, domain, or infrastructure cell cannot dominate the
  sender's evidence window.

### 7.2 Optimization objectives

Candidates are scored for:

1. closing the sender's destination-provider coverage gaps;
2. pair and domain novelty;
3. observer trust and evidence independence;
4. balanced recipient load and sentinel exposure;
5. thread continuity when a reply is due;
6. time-zone realism;
7. content and conversation diversity;
8. avoiding correlated worker, relay, MTA, ASN, and tenant fault domains.

The allocator stores the selected constraints and score components on the lease.
This makes a bad match reproducible in an incident review.

### 7.3 Pair leases

A short-lived, single-use `warmup_pair_leases` row authorizes one action. The send
claim must reference the lease. The lease includes a nonce, expiry, lane, policy
version, and signed sender/recipient binding. It prevents a stale worker from
sending after a quarantine or reassignment.

## 8. Correlated failure and propagation controls

The evaluator aggregates observations over mailbox, domain, DKIM domain, relay,
observed MTA/ASN, worker, content version, and destination-provider route.

Examples:

- one mailbox degrades across Gmail and Microsoft: isolate the mailbox;
- several domains degrade only through one relay/MTA: quarantine that route and
  preserve unrelated routes;
- one domain's mailboxes degrade across independent relays: cap or quarantine the
  domain;
- many senders degrade only at Microsoft: open a provider-route incident and
  reduce that route rather than poisoning every sender's global state;
- one content generator version correlates with spam: retire the content version;
- one observer reports anomalous spam for otherwise clean senders: reduce observer
  trust before punishing the senders.

Propagation is evidence-gated. A child incident can reduce a parent's risk budget,
but cannot automatically quarantine unrelated siblings without independent
corroboration. Every fault domain has an exposure budget and circuit breaker.

## 9. Shared network without breaking Inroad's tenancy model

The current workspace-local pool stays the default. A shared cloud or federated
pool is a separate coordinator service and data store, not a cross-workspace SQL
query added to the Inroad database.

```text
Inroad control plane
  -> publishes signed, explicitly opted-in participant advertisement
  -> requests pair lease through Coordinator interface
  <- receives minimum routing data + signed lease

Inroad worker
  -> gets the assignment only through coreapi
  -> uses the mailbox credential already held by its own workspace
  -> reports signed outcome through coreapi

Reputation coordinator
  -> stores pseudonymous participant/fault-domain IDs and pool telemetry
  -> never receives mailbox credentials or workspace DEKs
  -> applies admission, allocation, exposure, and recovery policy
```

Cross-tenant warmup necessarily exposes sender and recipient email addresses to
the two participating mail systems. It must therefore be explicit opt-in with
clear UI disclosure and a removable network membership. No mailbox credential,
OAuth token, campaign data, contact data, or workspace encryption material crosses
the boundary.

The coordinator is injected behind an interface:

- `LocalCoordinator`: current workspace-local behavior, upgraded with lanes and
  confidence;
- `RemoteCoordinator`: mutually authenticated, signed lease/report protocol;
- `FederatedCoordinator`: optional later implementation with node admission and
  signed observer attestations.

## 10. Persistence model

New tables are workspace-pinned unless explicitly marked as global infrastructure.
Composite tenant foreign keys remain mandatory.

### Control-plane tables

- `warmup_memberships`: lifecycle state, lane, policy version, state version,
  cooldown, and admission metadata;
- `warmup_identity_facts`: provider, domain, DKIM/return-path identities, worker,
  observed relay/MTA/ASN, timezone, and auth posture;
- `warmup_pair_leases`: sender, recipient, lane, nonce, expiry, constraint snapshot,
  status, and policy version;
- `warmup_observations`: immutable normalized outcomes;
- `warmup_reputation_windows`: subject type/id, destination provider, metric,
  estimate, credible bounds, effective sample, diversity counts, and freshness;
- `warmup_state_transitions`: append-only from/to lane and lifecycle, reason code,
  metric snapshot, policy version, actor, and timestamp;
- `warmup_exposure_budgets`: lane/fault-domain traffic limits and current usage;
- `warmup_incidents`: correlated-failure scope, evidence, state, and resolution.

Do not delete existing `warmup_daily_stats` initially. Rebuild it as a projection
from observations and migrate APIs gradually.

## 11. Policy versioning and explainability

Every lease, reputation snapshot, and transition stores a policy version. Policy
evaluation returns structured reasons, not only a sentence:

```json
{
  "action": "move_to_recovery",
  "reason_code": "spam_upper_bound_exceeded",
  "subject": { "type": "mailbox", "id": "..." },
  "metric": "spam_placement",
  "estimate": 0.08,
  "upper_95": 0.14,
  "effective_sample": 31.6,
  "threshold": 0.10,
  "policy_version": "warmup-2026-09-1"
}
```

The dashboard should show state, confidence, route-specific evidence, fault-domain
incidents, and the exact requirements for the next transition. Operators should
never see only “health 72”.

## 12. Rollout

### Phase 0: fix dangerous ambiguity in the current engine

- add `unknown` and minimum effective sample handling;
- persist and evaluate invalid-token, hard-bounce, and complaint signals;
- add immutable observation and transition history;
- enforce an explicit pair cooldown and pair frequency cap;
- stop treating an empty placement window as healthy.

### Phase 1: local safety controller

- split lifecycle, pool lane, sender health, and observer trust;
- add probation, quarantine, recovery, and blocked behavior;
- add auth prerequisites and confidence-qualified recovery;
- implement the constrained allocator for workspace-local pools;
- gate new campaign leads through mailbox and domain policy.

### Phase 2: fault-domain intelligence

- normalize provider-native placement beyond inbox/spam/other;
- extract DKIM, return-path, observed MTA IP/ASN, and destination-provider route;
- add route matrices, correlated incidents, exposure budgets, and content-version
  attribution;
- add sentinels and observer calibration.

### Phase 3: optional shared reputation network

- ship the `Coordinator` protocol and remote adapter;
- add explicit network consent, admission controls, node identity, quotas, and
  signed reports;
- start with one small, curated healthy cell and a separate recovery cell;
- expand only when sentinel capacity and incident operations can protect the
  promised isolation.

### Phase 4: adaptive optimization

- tune allocation weights from controlled experiments;
- forecast reputation risk before a threshold is crossed;
- recommend campaign ramp changes from provider-route capacity;
- keep deterministic policy fallbacks so ML is never required for safe operation.

## 13. Acceptance criteria

The design is complete when the system can prove all of the following:

1. An unknown or quarantined participant cannot send to or receive from a healthy
   customer mailbox.
2. Time passing alone cannot return a participant to the healthy lane.
3. A mailbox with no recent observations is unknown, not healthy.
4. Every automated transition names its metric, sample, uncertainty, threshold,
   policy version, and evidence window.
5. One observer, pair, domain, provider, worker, or relay cannot dominate a health
   decision.
6. A provider-specific incident can be contained without pausing healthy routes.
7. A stale pair lease cannot send after quarantine.
8. Shared-pool coordination never exposes mailbox credentials, workspace DEKs,
   campaign data, or contacts.
9. Self-hosted local warmup continues to work without the remote coordinator.
10. Campaign capacity is the minimum safe budget across mailbox, domain, relay,
    infrastructure, and destination-provider route.

## 14. Non-goals

- claiming warmup guarantees inbox placement;
- optimizing for the largest possible pool;
- using opens or clicks as primary reputation evidence;
- allowing an AI model to override hard safety gates;
- silently enrolling self-hosted mailboxes into a shared network;
- assuming worker IP affinity equals destination-visible sending-IP affinity.
