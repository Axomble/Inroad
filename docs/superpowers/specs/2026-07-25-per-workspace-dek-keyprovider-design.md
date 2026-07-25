# Per-Workspace DEK + KeyProvider Seam (Design)

**Date:** 2026-07-25
**Branch:** TBD `feature/per-workspace-dek` (off `main`)
**Status:** Design — pending review

## 1. Goal

Move Inroad from a single global field key to a **two-level key hierarchy with a
per-workspace data-encryption key (DEK)**, behind a `KeyProvider` (KEK) seam so a
cloud KMS becomes a drop-in later. This is the roadmap's one architectural item
"worth prioritizing for its own sake" (competitive analysis 02 §4 / 03), and it
directly hardens the Gmail/M365 OAuth refresh tokens just shipped — today they sit
under one global `crypto.Sealer` master key.

Grounded in industry best practice (AWS Encryption SDK, Google Cloud KMS): envelope
encryption, AES-256-GCM, ciphertext bound to context via AAD, self-describing
provider-tagged envelope. One deliberate divergence from the AWS/Google *default*
(fresh DEK per write): we keep a **per-workspace** DEK to enable **crypto-shredding
on workspace deletion**.

Non-goals this phase: the cloud-KMS `KeyProvider` implementation (interface only);
full AWS-ESDK key-commitment (note it; AAD binding gives the practical benefit); a
rotation CLI (design the re-seal path, ship the command later).

## 2. Key hierarchy

```
field plaintext (SMTP password / OAuth token JSON)
    │  AES-256-GCM, AAD = "workspace:<id>|purpose:mailbox-secret"
    ▼
sealed under the workspace DEK (32 bytes, one per workspace)
    │  Wrap()  ← KeyProvider (KEK)
    ▼
wrapped DEK stored in workspace_deks.wrapped_dek
    KeyProvider = local (AES-256-GCM under INROAD_MASTER_KEY)  |  cloud KMS (future)
```

- **KEK** = the key that wraps DEKs. `INROAD_MASTER_KEY` is **repurposed from
  field-key → KEK** (it now wraps DEKs, never touches field data directly except in
  the legacy v1 path below).
- **DEK** = per-workspace 32-byte key that seals field data. Generated once per
  workspace, cached in memory, never stored in plaintext.

## 3. `KeyProvider` interface (the KMS seam)

New `internal/platform/crypto/keyprovider.go`:

```go
type KeyProvider interface {
    // Wrap encrypts a plaintext DEK for storage. Returns opaque bytes.
    Wrap(ctx context.Context, dek []byte) (wrapped []byte, err error)
    // Unwrap reverses Wrap.
    Unwrap(ctx context.Context, wrapped []byte) (dek []byte, err error)
    // Name identifies the provider; recorded read-only with each wrapped DEK so a
    // provider switch is an explicit re-wrap, never a silent one.
    Name() string
}
```

- **`LocalKeyProvider`** — AES-256-GCM under `INROAD_MASTER_KEY`. `Name() == "local"`.
  Default; zero operational change for self-hosters.
- **Cloud KMS** — future drop-in (`Name() == "aws-kms"`, etc.). Interface added now
  because the value (per-workspace DEK isolation + crypto-shred) exists with only the
  local provider — this is the documented seam, not speculative single-impl indirection.

## 4. `workspace_deks` table (migration `000013`)

```sql
CREATE TABLE workspace_deks (
    workspace_id  uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    wrapped_dek   bytea       NOT NULL,
    key_provider  text        NOT NULL,          -- which KeyProvider wrapped it
    created_at    timestamptz NOT NULL DEFAULT now()
);
```

- **INSERT is fail-if-exists** (PK) — never overwrite a DEK (the hard-won lesson:
  overwriting silently invalidates every prior ciphertext). Rotation is a separate
  explicit re-encrypt path, not an upsert.
- `ON DELETE CASCADE` from `workspaces` — deleting a workspace drops its DEK →
  **crypto-shredding**: all that workspace's sealed data becomes unrecoverable by
  construction, even if the ciphertext rows linger in backups.
- `key_provider` recorded so a future KMS migration re-wraps deliberately.

## 5. `Keyring` — DEK lifecycle + cache

New `internal/platform/crypto/keyring.go`:

```go
type DEKStore interface {                        // satisfied by the sqlc-backed store
    GetWrappedDEK(ctx, workspaceID uuid.UUID) (wrapped []byte, provider string, err error)
    PutWrappedDEK(ctx, workspaceID uuid.UUID, wrapped []byte, provider string) error // fail-if-exists
}

type Keyring struct { provider KeyProvider; store DEKStore; cache *ttlCache }

// SealerFor returns a Sealer bound to workspaceID's DEK, creating+persisting the DEK
// on first use. The returned Sealer seals/opens field data under that DEK.
func (k *Keyring) SealerFor(ctx context.Context, workspaceID uuid.UUID) (*Sealer, error)
```

- First use for a workspace → generate 32-byte DEK (`crypto/rand`), `Wrap`, `Put`
  (fail-if-exists; on a race, re-read and unwrap the winner), cache the plaintext DEK.
- Thereafter → `Get` wrapped DEK, `Unwrap`, cache.
- **Cache** = in-memory TTL map (e.g. 5-min), keyed by workspace id. No Redis (simpler
  than the competitor's Redis cache; fine at our scale). Cache holds plaintext DEKs —
  process-memory only, evicted on TTL; documented as an accepted, bounded exposure
  (same class as the already-in-memory decrypted secrets).

## 6. `Sealer` changes — AAD + self-describing versioned envelope

`crypto.Sealer` keeps AES-256-GCM but the API gains context and the blob becomes
self-describing:

```go
// Seal binds the ciphertext to workspaceID + purpose via GCM AAD.
func (s *Sealer) Seal(plaintext []byte, aad []byte) (string, error)
func (s *Sealer) Open(token string, aad []byte) ([]byte, error)
```

- **AAD** = `"ws:<workspaceID>|p:<purpose>"` (e.g. purpose `mailbox-secret`). A blob
  sealed for one workspace cannot be opened in another's context — GCM auth fails.
- **Blob format (v2):** `0x02 || nonce || ciphertext`, base64. The DEK is *not* inside
  the field blob (it lives wrapped in `workspace_deks`, one per workspace) — the version
  byte + the Keyring's provider tag make decryption self-describing at the DEK layer.
- **Legacy (v1):** existing `secret_ciphertext` values have **no version prefix** and
  are sealed directly under the old master key. `Open` detects "no `0x02` prefix / not
  our v2 shape" → decrypts via a **legacy path** using the master key directly (the
  master key is still loaded, now as the KEK). On the next write, the value re-seals as
  v2 under the workspace DEK (lazy migration). An optional one-time backfill command
  can migrate eagerly. No downtime, correct with existing rows.

> Version byte lets us evolve (e.g. add key commitment as v3) without breaking v1/v2.

## 7. Refactor scope

- `mailbox.Service`: replace the injected `*crypto.Sealer` with `*crypto.Keyring`.
  `ConnectSMTP` / `CompleteGoogleOAuth` / `CompleteMicrosoftOAuth` →
  `k.SealerFor(ctx, ws).Seal(raw, aadFor(ws))`.
- `inprocess` coreapi: replace `c.sealer` with `c.keyring`; `oauthAccessToken`,
  `GetSendJob`, `GetStepSendJob`, `GetInboxPollJob` → resolve `SealerFor(ctx, ws)`
  then `Open`/`Seal` with the workspace AAD. All sites already have `ws`.
- Wiring (`cmd/inroad`, `cmd/worker`): build `LocalKeyProvider` from `INROAD_MASTER_KEY`,
  a `Keyring` over the sqlc DEK store, inject it where the sealer went.
- **Unchanged:** `MultiSender`, senders/readers, OAuth flows, all queue/worker logic —
  only key resolution moves. The `Sealer` primitive stays AES-256-GCM.

## 8. Crypto-shredding (bonus capability this unlocks)

Workspace deletion already cascades; with `workspace_deks ON DELETE CASCADE`, deleting
a workspace destroys its DEK, rendering all its sealed secrets permanently
unrecoverable — a concrete GDPR erasure story we get "for free." Documented in
`docs/security.md`; no separate delete-path code needed beyond the FK.

## 9. Security invariants (docs/security.md)

- Field data is sealed under a **per-workspace DEK**, itself wrapped by the KEK
  (`KeyProvider`); plaintext DEKs live only in a short-TTL in-memory cache, never on disk.
- Every field ciphertext is **AAD-bound to its `workspace_id`** — cross-tenant decrypt
  fails closed.
- **Never overwrite a DEK** (fail-if-exists); provider recorded read-only; rotation is
  explicit re-encrypt.
- `INROAD_MASTER_KEY` is now the KEK; losing it loses every DEK (documented, same blast
  radius as before — it was already the single field key).
- Legacy v1 blobs remain decryptable and lazily migrate to v2; no plaintext-at-rest window.

## 10. Config

- `INROAD_MASTER_KEY` — unchanged (now the KEK; still 32 bytes base64).
- `INROAD_KEY_PROVIDER` — default `local`. Selects the `KeyProvider` impl.
- No breaking change; existing deployments keep working, secrets migrate lazily.

## 11. Testing (our edge — theirs ships untested)

- **Unit:** `LocalKeyProvider` wrap/unwrap round-trip + tamper; `Keyring.SealerFor`
  create-then-reuse, fail-if-exists on race, cache hit/miss; `Sealer` v2 seal/open,
  **AAD mismatch → open fails**, legacy v1 open, v1→v2 re-seal on write, wrong-provider
  wrapped DEK rejected; version-byte forward-compat.
- **Integration (Postgres):** DEK persisted + reused across calls; **cross-workspace
  isolation** (workspace A's blob won't open under B's DEK/AAD); crypto-shred (delete
  workspace → DEK gone → open fails); existing-row legacy decrypt then re-seal.
- **Backward-compat:** all existing mailbox/OAuth send/poll tests stay green with the
  Keyring swapped in.

## 12. Delivery order (independently testable)

1. `KeyProvider` interface + `LocalKeyProvider` + tests.
2. Migration `000013` `workspace_deks` + sqlc DEK store (`GetWrappedDEK`/`PutWrappedDEK` fail-if-exists).
3. `Keyring` (create/cache/SealerFor) + `Sealer` AAD + versioned v1/v2 envelope + tests.
4. Refactor call sites (`mailbox.Service`, `inprocess`) + `cmd/*` wiring; all send/poll tests green.
5. Docs (`security.md` invariants + crypto-shred, `self-hosting.md` `INROAD_KEY_PROVIDER`) + `.env.example`.

Follow-ups (separate phases): cloud-KMS `KeyProvider` impl; eager re-seal/rotation CLI;
AES-GCM key commitment (v3) if the threat model warrants.

## 13. References

- AWS Encryption SDK — envelope encryption, encryption context (AAD), self-describing
  encrypted message, key commitment: https://docs.aws.amazon.com/encryption-sdk/latest/developer-guide/concepts.html
- Google Cloud KMS — envelope encryption, DEK/KEK, AES-256-GCM, DEK-per-write guidance:
  https://docs.cloud.google.com/kms/docs/envelope-encryption
- Competitive analysis 02 §4 (per-org DEK + KMS) / 03 (P1 DEK seam), this repo.
