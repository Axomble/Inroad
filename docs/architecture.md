# Inroad Architecture

See `docs/superpowers/specs/2026-07-19-outpost-repo-architecture-design.md` for the
full layout rationale. This document tracks decisions as they evolve during build.

## Planes
- **Control plane:** API server + Postgres + Redis.
- **Execution plane:** worker(s), reaching data only through `internal/coreapi`.

## Authentication & sessions
- **Multi-workspace identity:** Users are accounts belonging to multiple workspaces via `workspace_members` (roles: owner, admin, member).
- **Token model:** Short-lived access tokens (JWT HS256, default 15 minutes) sent as `Authorization: Bearer` and held in memory on the SPA. Long-lived refresh tokens (opaque, default 30 days) stored in an httpOnly `inroad_refresh` cookie (Path=/api/v1/auth), rotated on refresh with reuse detection — replaying a revoked token revokes the entire session family.
- **CSRF protection:** Double-submit token (`csrf_token` cookie + `X-CSRF-Token` header) on cookie endpoints (/auth/refresh, /auth/logout).
- **Authorization:** Deny-by-default — all non-public routes require a valid access token. Public endpoints: POST /register, /login, /refresh, /logout.

## Credential encryption (two-level key hierarchy)
Stored field secrets are protected by a two-level key hierarchy:
**field ciphertext ← per-workspace DEK ← KEK (`KeyProvider`)**.

- **DEK (data-encryption key):** a random 32-byte AES-256 key per workspace.
  The `crypto.Sealer` seals each field (SMTP password, OAuth token) under the
  workspace's DEK with the workspace id as AES-GCM AAD, in a versioned
  self-describing envelope (v2 = `0x02 || nonce || ct`; legacy v1 = master-key
  direct, no version byte). `crypto.Keyring.SealerFor(ctx, ws)` hands out a
  workspace-bound `*Sealer`, generating the DEK on first use and caching the
  plaintext DEK in a short-TTL in-memory cache (zeroized on eviction).
- **KEK (key-encryption key):** the `crypto.KeyProvider` seam wraps/unwraps DEKs.
  `LocalKeyProvider` wraps under an HKDF-derived subkey of `INROAD_MASTER_KEY`
  (`inroad-kek-v1`); a cloud KMS is a future drop-in. Only the wrapped DEK is
  persisted, in `workspace_deks` (fail-if-exists PK, `key_provider` recorded).
- **Wiring seam:** `internal/platform/keys` holds the `PgDEKStore` (sqlc-backed
  `crypto.DEKStore` adapter over `*gen.Queries`) and `BuildKeyring(cfg, q)`,
  which selects the provider from `cfg.KeyProvider` (fail-closed on an unknown
  value) and assembles the `Keyring`. It lives in `platform` so both composition
  roots (`cmd/inroad`, `cmd/worker`) can wire it without `crypto` importing
  `db/gen`; the worker still reaches decrypted data only via `coreapi`, which
  holds the injected `Keyring`.
- **Crypto-shredding:** `workspace_deks.workspace_id` is `ON DELETE CASCADE` on
  `workspaces`, so deleting a workspace destroys its DEK and permanently renders
  all of its sealed data unrecoverable (see `docs/security.md` invariants 14–19).

## Mail providers (SMTP + Gmail + M365)
A mailbox's `provider` column (`smtp` | `gmail` | `m365`) is the transport discriminator; the abstraction keeps the worker's seams single-branched. M365 joins as a second OAuth provider behind the same seams, reusing the sealed-token codec, control-plane refresh, and opaque cursor unchanged — only the Graph-specific auth, send, and delta differ.
- **Send:** the worker calls one seam, `mail.MultiSender.Send(ctx, OutboundJob, Message)`. `MultiSender` dispatches on `OutboundJob.Provider` — SMTP via `NetSender` (through the SSRF guard + TLS), Gmail via `GmailSender` (Gmail API, fixed Google host), M365 via `GraphSender` (Microsoft Graph `sendMail`, fixed Graph host). Both `sender.Handler` and `sequence.AdvanceHandler` build one `OutboundJob` from the coreapi job, so the transport branch lives in exactly one place. `GmailSender` reuses `buildMessage` and returns our own `Message-ID` header (Gmail preserves it); `GraphSender` also reuses `buildMessage` but uses a draft-then-send flow (Exchange may rewrite the `Message-Id`, so it reads back and returns the authoritative `internetMessageId`), so threading and reply matching are identical across transports.
- **Inbox:** `GmailReader` and `GraphReader` parallel the IMAP `InboxReader` for reply/bounce polling; the inbox worker dispatches on `provider`. RFC822 parsing, DSN/bounce detection, and reply matching are transport-agnostic and shared unchanged.
- **Token lifecycle:** OAuth tokens are sealed into `secret_ciphertext` (the same column SMTP passwords use). `coreapi` (inprocess) refreshes the token at job-build time via the provider-neutral `oauthAccessToken` — it selects the Google or Azure AD `oauth2.Config` by `provider`, holds the pool + sealer, re-seals a rotated token, and hands the worker only a short-lived access token on the job (`Provider` + `AccessToken []byte`, zeroized after use). The worker never refreshes or persists. MS OAuth uses `golang.org/x/oauth2` with the Azure AD endpoint (`microsoft.AzureADEndpoint`, tenant default `common`).
- **Cursor:** Gmail tracks inbox position by an opaque, monotonic `historyId`; M365 tracks it by an opaque Graph delta/next-link URL. Both share the single `mailboxes.inbox_cursor` column (`SetInboxCursorString`), alongside — not replacing — the IMAP UID/UIDVALIDITY cursor columns, so the paths never collide. The M365 delta cursor is host-pinned to `graph.microsoft.com` before it is re-dialed with the mailbox bearer (see `docs/security.md` invariant 13).
