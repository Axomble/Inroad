---
title: Outbound Webhooks
description: Registering signed webhook endpoints, the v1 event catalog and payloads, HMAC signature verification, and the delivery retry schedule.
---

Outbound webhooks let a self-hosted deployment drive its own automation off Inroad
events — a reply landed, an address hard-bounced, a contact opted out — without
polling the REST API. A workspace registers an HTTPS endpoint; the control plane
fans a small, versioned event catalog out to it as signed `POST` requests
(`internal/app/webhook`, `internal/worker/webhook`).

---

## Registering an endpoint

`POST /api/v1/webhook-endpoints` with `{ "url": "https://example.com/hooks/inroad" }`.

- `url` must be `http` or `https` and must **not** resolve to a loopback,
  private (RFC1918 / ULA), link-local (incl. the cloud metadata address
  `169.254.169.254`), unspecified, or multicast address. The check runs at
  create, at update, and again in the worker immediately before every delivery
  (closing the DNS-rebinding window). `INROAD_WEBHOOK_ALLOW_PRIVATE=true` relaxes
  only the loopback/private part, for local development.
- `event_types` is an optional array from the catalog below. An empty or omitted
  list subscribes to **every** event.
- The response carries `secret` **once**: a base64 32-byte HMAC signing key.
  Store it now — it is not recoverable. `POST /api/v1/webhook-endpoints/{id}/rotate-secret`
  mints a new one (and immediately invalidates the old).

`active: false` (via `PATCH`) silences an endpoint without losing its
configuration or secret. `POST /api/v1/webhook-endpoints/{id}/ping` queues a
synthetic `ping` delivery regardless of the subscription filter, so you can
confirm a receiver is reachable and verifies signatures.

---

## Request shape

Every delivery is a `POST` with body:

```json
{
  "id": "<delivery uuid>",
  "event": "<event type>",
  "occurred_at": "<RFC3339 timestamp>",
  "data": { }
}
```

Headers:

| Header | Value |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `Inroad-Webhooks/1` |
| `Inroad-Event` | the event type |
| `Inroad-Delivery-Id` | the delivery uuid |
| `Inroad-Webhook-Id` | the endpoint uuid |
| `Inroad-Signature` | `t=<unix>,v1=<hex hmac-sha256(secret, "<t>.<rawBody>")>` |

The `POST` times out after 10 seconds. Only a `2xx` response is treated as
success; `3xx` redirects are **not** followed.

---

## Verifying the signature

`Inroad-Signature` carries a Unix timestamp `t` and a hex HMAC-SHA256 `v1`. To
verify: split the header on `,`; compute
`HMAC_SHA256(secret, t + "." + rawRequestBody)`; compare it to `v1` in constant
time; and reject the request if `t` is not recent (a few minutes' tolerance
bounds replay). The timestamp is bound into the MAC, so a captured signature
cannot be reused against a different body.

---

## Retry schedule

A delivery that fails (transport error, or any non-`2xx`) is retried on this
schedule, up to **6 attempts total**:

| After attempt | Wait before next |
| --- | --- |
| 1 | 1 minute |
| 2 | 5 minutes |
| 3 | 30 minutes |
| 4 | 2 hours |
| 5 | 6 hours |

After the 6th failed attempt the delivery is marked `failed` and not retried
again. Each attempt is recorded in the delivery log
(`GET /api/v1/webhook-endpoints/{id}/deliveries`, keyset-paginated), which keeps
`status`, `attempts`, `last_error`, `response_status`, and `delivered_at`.
Deliveries older than 30 days are purged by the daily maintenance job.

---

## Event catalog (v1)

`data` carries ids and minimal display fields only — never a credential, never a
full message body.

### `reply.received`

A human inbound reply was recorded against a thread.

```json
{
  "reply": {
    "id": "<message id>",
    "from": "prospect@example.com",
    "to": "sender@yourco.com",
    "subject": "Re: quick question",
    "snippet": "Sure, let's talk Thursday...",
    "classification": "interested",
    "received_at": "2026-09-07T13:20:00Z"
  },
  "thread_id": "<uuid>",
  "campaign_id": "<uuid or null>",
  "contact_id": "<uuid or null>",
  "mailbox_id": "<uuid>"
}
```

`snippet` is at most 500 characters.

### `email.bounced`

A hard bounce (DSN) was finalized: the address is now suppressed.

```json
{
  "bounce": { "type": "hard", "diagnostic": "", "reported_at": "2026-09-07T13:20:00Z" },
  "email": "invalid@example.com",
  "enrollment_id": "<uuid, present when the bounce matched an enrollment>"
}
```

### `contact.unsubscribed`

An opt-out was recorded — either through the one-click List-Unsubscribe endpoint
(`source: "one_click"`) or classified from a reply (`source: "reply"`).

```json
{
  "contact_id": null,
  "email": "prospect@example.com",
  "reason": "unsubscribe",
  "source": "one_click",
  "occurred_at": "2026-09-07T13:20:00Z"
}
```
