# Per-Workspace DEK + KeyProvider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace the single global field key with a two-level hierarchy — field data sealed under a per-workspace DEK, the DEK wrapped by a KEK behind a `KeyProvider` seam — with ciphertext AAD-bound to `workspace_id` and legacy blobs migrating lazily.

**Architecture:** `LocalKeyProvider` (AES-256-GCM under `INROAD_MASTER_KEY`, now the KEK) wraps a per-workspace 32-byte DEK stored in `workspace_deks`; a `Keyring` creates/caches DEKs and returns a workspace-bound `*Sealer`; the `Sealer` AES-256-GCM-seals fields under the DEK with `workspace_id` as AAD, in a versioned self-describing envelope (v1 legacy = master-key-direct, v2 = DEK). Spec: `docs/superpowers/specs/2026-07-25-per-workspace-dek-keyprovider-design.md`.

**Tech Stack:** Go 1.25 · crypto/aes + crypto/cipher (AES-256-GCM) · pgx/sqlc · golang-migrate.

## Global Constraints

- Toolchain: prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"` in the SAME command. Do NOT `set -a && . ./.env` before tests (pollutes config-default tests; the integration harness defaults `INROAD_DATABASE_URL` to `localhost:5433`).
- Go files lowercase; identifiers `MixedCaps`; snake_case only at DB/env boundaries.
- Secrets never logged; plaintext DEKs live only in the in-memory cache, never on disk/response.
- Worker reaches data only via `coreapi` (zero `db` import) — the Keyring's `DEKStore` is satisfied by sqlc and injected; the worker gets the Keyring through `inprocess`, which already holds the pool.
- Migrations/queries under `internal/platform/db/`; regenerate with `sqlc generate` (the `make sqlc` target). Migration head is `000012`; new migration is `000013`.
- Conventional commits; do NOT commit (coordinator commits per task). Verify before "done": `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (only the 4 known pre-existing dirty files may appear), `go test ./...`.
- The `crypto.Sealer` AES-256-GCM primitive stays; only key provenance + AAD + envelope versioning change.

**Reference (read first):** `internal/platform/crypto/sealer.go` (current Sealer), `internal/platform/crypto/sealer_test.go`, `cmd/inroad/main.go:57,94`, `cmd/worker/main.go:36,55`, `internal/app/mailbox/{service.go,oauth.go}`, `internal/coreapi/inprocess/{inprocess.go,oauthtoken.go,sendjob.go,stepsendjob.go,inboxpoll.go}`.

---

### Task 1: `KeyProvider` interface + `LocalKeyProvider`

**Files:** Create `internal/platform/crypto/keyprovider.go` + `keyprovider_test.go`.

**Interfaces (Produces):**
- `crypto.KeyProvider` interface: `Wrap(ctx, dek []byte) ([]byte, error)`, `Unwrap(ctx, wrapped []byte) ([]byte, error)`, `Name() string`.
- `crypto.NewLocalKeyProvider(masterKey []byte) (*LocalKeyProvider, error)` — validates 32-byte key; `Name() == "local"`.

- [ ] **Step 1:** Write `keyprovider_test.go`: wrap→unwrap round-trip returns the original 32-byte DEK; a tampered wrapped blob fails Unwrap; `Name()=="local"`; `NewLocalKeyProvider` rejects a non-32-byte key.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Write `keyprovider.go`:

```go
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// KeyProvider is the key-encryption-key (KEK) seam: it wraps and unwraps
// per-workspace data-encryption keys (DEKs). LocalKeyProvider is the default
// (AES-256-GCM under INROAD_MASTER_KEY); a cloud KMS is a future drop-in.
type KeyProvider interface {
	Wrap(ctx context.Context, dek []byte) (wrapped []byte, err error)
	Unwrap(ctx context.Context, wrapped []byte) (dek []byte, err error)
	Name() string
}

// LocalKeyProvider wraps DEKs with AES-256-GCM under the local master key.
type LocalKeyProvider struct{ aead cipher.AEAD }

func NewLocalKeyProvider(masterKey []byte) (*LocalKeyProvider, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &LocalKeyProvider{aead: aead}, nil
}

func (p *LocalKeyProvider) Name() string { return "local" }

func (p *LocalKeyProvider) Wrap(_ context.Context, dek []byte) ([]byte, error) {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, dek, nil), nil
}

func (p *LocalKeyProvider) Unwrap(_ context.Context, wrapped []byte) ([]byte, error) {
	ns := p.aead.NonceSize()
	if len(wrapped) < ns {
		return nil, errors.New("wrapped dek too short")
	}
	return p.aead.Open(nil, wrapped[:ns], wrapped[ns:], nil)
}
```

- [ ] **Step 4:** Run tests → PASS. `go build ./...`.
- [ ] **Step 5:** Commit `feat(crypto): KeyProvider seam + LocalKeyProvider (KEK)`.

---

### Task 2: Migration `000013` `workspace_deks` + DEK store queries

**Files:** Create `internal/platform/db/migrations/000013_workspace_deks.{up,down}.sql`; modify `internal/platform/db/queries/` (new `dek.sql`); regen `gen/`.

**Interfaces (Produces):** sqlc methods `GetWorkspaceDEK(ctx, workspaceID) (GetWorkspaceDEKRow{WrappedDek []byte, KeyProvider string}, error)` and `CreateWorkspaceDEK(ctx, CreateWorkspaceDEKParams{WorkspaceID, WrappedDek, KeyProvider}) error` (fail-if-exists via PK).

- [ ] **Step 1:** `000013_workspace_deks.up.sql`:

```sql
-- Per-workspace data-encryption key (DEK), wrapped by the KEK (KeyProvider).
-- The plaintext DEK is never stored. ON DELETE CASCADE gives crypto-shredding:
-- deleting a workspace destroys its DEK, rendering all its sealed data
-- (mailbox creds, OAuth tokens) permanently unrecoverable.
CREATE TABLE workspace_deks (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    wrapped_dek  bytea       NOT NULL,
    key_provider text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
```

- [ ] **Step 2:** `000013_workspace_deks.down.sql`: `DROP TABLE workspace_deks;`
- [ ] **Step 3:** `internal/platform/db/queries/dek.sql`:

```sql
-- name: GetWorkspaceDEK :one
SELECT wrapped_dek, key_provider FROM workspace_deks WHERE workspace_id = $1;

-- name: CreateWorkspaceDEK :exec
-- Fail-if-exists: the PK rejects an overwrite. A DEK is never replaced in place
-- (that would silently invalidate all prior ciphertext); rotation is an explicit
-- re-encrypt path, not implemented here.
INSERT INTO workspace_deks (workspace_id, wrapped_dek, key_provider)
VALUES ($1, $2, $3);
```

- [ ] **Step 4:** `sqlc generate && go build ./...`. Confirm the two methods + `WorkspaceDek` model in `gen/`.
- [ ] **Step 5:** Migration reversibility against the running DB (Docker up): `go run ./cmd/migrate up && (echo y | go run ./cmd/migrate down) && go run ./cmd/migrate up`. If Docker is down, report DEFERRED with the connection error — do not start Docker.
- [ ] **Step 6:** Commit `feat(db): migration 000013 workspace_deks + DEK store queries`.

---

### Task 3: `Keyring` + cache + `Sealer` AAD + versioned v1/v2 envelope

**Files:** Modify `internal/platform/crypto/sealer.go`; Create `internal/platform/crypto/keyring.go` + `keyring_test.go`; extend `sealer_test.go`.

**Interfaces (Produces):**
- `crypto.DEKStore` interface: `GetWrappedDEK(ctx, ws uuid.UUID) (wrapped []byte, provider string, err error)`, `PutWrappedDEK(ctx, ws uuid.UUID, wrapped []byte, provider string) error` (fail-if-exists; returns a sentinel/duplicate error on conflict).
- `crypto.NewKeyring(provider KeyProvider, store DEKStore, legacy *Sealer) *Keyring`.
- `(*Keyring).SealerFor(ctx context.Context, ws uuid.UUID) (*Sealer, error)` — returns a workspace-bound Sealer.
- `Sealer` gains an `aad []byte` (the workspace binding) and a `legacy *Sealer` (for v1 open). `Seal`/`Open` keep their **current single-arg signatures** (`Seal(plaintext []byte) (string, error)`, `Open(token string) ([]byte, error)`) so call-site churn is minimal; AAD + versioning are internal.

**Design notes for the implementer:**
- Keep `NewSealer(masterKey []byte)` — it builds the **legacy** master-key Sealer (`aad=nil`, `legacy=nil`) used both for the v1-decrypt fallback and, if ever needed, direct use. Add an unexported `newDEKSealer(dek, aad []byte, legacy *Sealer)`.
- **Envelope:** `Seal` prepends a version byte `0x02`, then `nonce || aes-gcm(dek, nonce, plaintext, aad)`, base64. `Open`: base64-decode; if first byte `== 0x02` → v2 path (DEK + aad); else → **legacy v1**: hand the *original token* to `s.legacy.Open` (master-key AEAD, no version byte, nil aad). v1 blobs have no `0x02` prefix by construction (old format is `base64(nonce||ct)`); guard the ambiguous case by trying v2 parse first and falling back to legacy on any structural/auth failure.
- **AAD** = `[]byte("ws:" + ws.String())`. Purpose is fixed (`mailbox-secret`) for now; if added later, fold into the AAD string.
- `SealerFor`: cache lookup (TTL map, 5-min) → hit returns `newDEKSealer(cachedDEK, aad, legacy)`. Miss → `GetWrappedDEK`; if found, `Unwrap`, cache, return. If not found, generate 32-byte DEK (`crypto/rand`), `Wrap`, `PutWrappedDEK` (on duplicate-error from a race, re-`GetWrappedDEK`+`Unwrap` the winner), cache, return.
- Cache: a small `sync.Mutex`-guarded `map[uuid.UUID]dekEntry{dek []byte; exp time.Time}`. Do NOT use `Date.now()`-style non-determinism concerns here — this is runtime Go, `time.Now()` is fine.

- [ ] **Step 1:** Write `keyring_test.go` + extend `sealer_test.go` (use a fake `DEKStore` — in-memory map with fail-if-exists):
  - `SealerFor` creates a DEK on first call, reuses it on the second (store `Put` called once).
  - Race: two concurrent `SealerFor` for a new ws → one `Put` wins, both return sealers whose Seal/Open interoperate.
  - v2 round-trip: `Seal` then `Open` returns plaintext; the blob starts with `0x02`.
  - **AAD mismatch:** a blob sealed for ws A fails to `Open` under ws B's sealer.
  - **Legacy v1:** a value sealed by the old `NewSealer(masterKey).Seal` opens via a workspace Sealer whose `legacy` is that master-key sealer.
  - fail-if-exists surfaced from the store is handled (race path).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `sealer.go` changes + `keyring.go` per the design notes.
- [ ] **Step 4:** Run crypto tests + `go build ./...` + `go vet ./...` → PASS.
- [ ] **Step 5:** Commit `feat(crypto): Keyring + per-workspace DEK Sealer (AAD-bound, versioned envelope)`.

---

### Task 4: Refactor call sites + wiring

**Files:** Modify `internal/app/mailbox/service.go` + `oauth.go` + `service_test.go`; `internal/coreapi/inprocess/inprocess.go` + `oauthtoken.go` + `sendjob.go` + `stepsendjob.go` + `inboxpoll.go`; `cmd/inroad/main.go`; `cmd/worker/main.go`; the 3 integration-test constructors.

**Interfaces (Consumes/Produces):**
- `mailbox.NewService(...)` param 3 changes `sealer *crypto.Sealer` → `keyring *crypto.Keyring`.
- `inprocess.New(...)` param 2 changes `sealer *crypto.Sealer` → `keyring *crypto.Keyring`.
- `inprocess` gets a `DEKStore` — the `gen.Queries` already in the client satisfy it via a thin adapter (or pass the Keyring pre-built from `cmd/worker`). Prefer building the Keyring in `cmd/*` and injecting it (keeps coreapi from constructing crypto policy).

**Design notes:**
- Each `Seal`/`Open` site: `sealer, err := <keyring>.SealerFor(ctx, ws); ... sealer.Seal(raw)` / `sealer.Open(ct)`. All sites already have `ws` (mailbox connect: `workspaceID`; coreapi: the parsed `ws`).
  - `mailbox/oauth.go` CompleteGoogleOAuth + CompleteMicrosoftOAuth (seal token) + `service.go` ConnectSMTP (seal password).
  - `inprocess/oauthtoken.go` `oauthAccessToken` (open + reseal-on-refresh) — resolve `SealerFor(ctx, ws)` once, use for both Open and the reseal Seal.
  - `inprocess/sendjob.go`, `stepsendjob.go`, `inboxpoll.go` (open the mailbox secret).
- `cmd/inroad/main.go` + `cmd/worker/main.go`: build `provider, _ := crypto.NewLocalKeyProvider(cfg.MasterKey)`; `legacy, _ := crypto.NewSealer(cfg.MasterKey)`; `keyring := crypto.NewKeyring(provider, dekStore, legacy)` where `dekStore` is the sqlc `*gen.Queries` wrapped to satisfy `DEKStore`. Inject `keyring` where `sealer` went. Select the provider by `cfg.KeyProvider` (default `local`; only `local` implemented — unknown value → fatal at startup).
- Update `internal/platform/config/config.go`: add `KeyProvider string` loaded from `INROAD_KEY_PROVIDER` (default `local`).
- Update `service_test.go` + the 3 integration constructors to pass a `*crypto.Keyring` (a test Keyring over a fake/in-memory DEKStore + `NewLocalKeyProvider(testKey)` + `NewSealer(testKey)` legacy).

- [ ] **Step 1:** Add `cfg.KeyProvider` + load. Add the `DEKStore` adapter over `*gen.Queries`.
- [ ] **Step 2:** Swap constructors + all Seal/Open sites. Update `cmd/*` wiring.
- [ ] **Step 3:** Fix all compile fallout (test constructors).
- [ ] **Step 4:** Verify: `sqlc generate && go build ./... && go vet ./... && gofmt -l internal cmd && go test ./...`. All existing mailbox/OAuth send/poll tests MUST stay green (they now go through per-workspace DEKs; a fresh test DB workspace gets a DEK on first seal).
- [ ] **Step 5:** Integration (Docker up): `go test -tags=integration -count=1 ./internal/app/mailbox/... ./internal/worker/... ./internal/coreapi/...` — confirm cross-workspace isolation + DEK persistence hold. If Docker down, report DEFERRED.
- [ ] **Step 6:** Commit `refactor(crypto): seal mailbox/OAuth secrets under per-workspace DEK via Keyring`.

---

### Task 5: Docs + config

**Files:** Modify `docs/security.md`, `docs/self-hosting.md`, `docs/architecture.md`, `.env.example`.

- [ ] **Step 1:** `security.md`: add invariants — field data sealed under a per-workspace DEK wrapped by the KEK (`KeyProvider`); ciphertext AAD-bound to `workspace_id` (cross-tenant decrypt fails closed); never overwrite a DEK (fail-if-exists), provider recorded read-only; `INROAD_MASTER_KEY` is now the KEK; crypto-shredding on workspace deletion via `ON DELETE CASCADE`; legacy v1 blobs decrypt + migrate lazily.
- [ ] **Step 2:** `self-hosting.md`: document `INROAD_KEY_PROVIDER` (default `local`); note the master key is now the KEK (unchanged operationally); mention cloud-KMS is a future provider.
- [ ] **Step 3:** `architecture.md`: note the two-level key hierarchy + `KeyProvider` seam.
- [ ] **Step 4:** `.env.example`: add `INROAD_KEY_PROVIDER=local`.
- [ ] **Step 5:** Commit `docs(crypto): per-workspace DEK + KeyProvider invariants + config`.

---

## Self-Review

- Spec coverage: §3 KeyProvider→T1, §4 table→T2, §5 Keyring→T3, §6 Sealer/AAD/envelope→T3, §7 refactor→T4, §8 crypto-shred→T2 (FK)+T5 (docs), §9 invariants→T5, §10 config→T4/T5, §11 tests→per task, §12 order→task order. Covered.
- No placeholders; each task has real code or exact insertion points + the constructor signatures verified against the current tree.
- Type consistency: `NewService`/`inprocess.New` param 2/3 `*crypto.Sealer`→`*crypto.Keyring` (T4 updates ALL call sites incl. cmd + 3 integration tests); `SealerFor(ctx, ws) (*Sealer, error)` used uniformly; `DEKStore` satisfied by `*gen.Queries` adapter; `Seal`/`Open` keep single-arg signatures so only the resolution line changes at each site.
- Migration `000013` verified as next free (head is `000012`).
