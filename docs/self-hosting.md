# Self-Hosting Inroad

## Requirements
- Docker + Docker Compose

## Run
    cp .env.example .env
    # Generate real secrets (the compose stack refuses to start without them):
    #   INROAD_JWT_SECRET   = openssl rand -base64 32
    #   INROAD_MASTER_KEY   = openssl rand -base64 32   (must decode to 32 bytes)
    # Authentication (optional; see .env.example for defaults):
    #   INROAD_ACCESS_TOKEN_TTL  = 15m     (access token lifetime)
    #   INROAD_REFRESH_TOKEN_TTL = 720h    (refresh token lifetime; default 30 days)
    #   INROAD_COOKIE_SECURE     = true    (set to false for local http development)
    #   INROAD_COOKIE_DOMAIN     =         (leave empty for localhost development)
    # Put all in .env, then:
    docker compose up --build

The API (with the built web UI) serves on http://localhost:8080. Migrations run
automatically on the api container's startup. The worker connects to Redis.

## Encryption keys

Field secrets (SMTP passwords, Gmail/M365 OAuth tokens) are sealed under a
per-workspace data-encryption key (DEK); each DEK is wrapped by a
key-encryption key (KEK) selected by `INROAD_KEY_PROVIDER`.

- `INROAD_KEY_PROVIDER` — the KEK backend. Defaults to `local` (blank is
  equivalent to `local`). `local` wraps DEKs under `INROAD_MASTER_KEY`. Any
  other value is rejected at startup with a fatal error (fail-closed) — only
  `local` is implemented today; a cloud KMS is a planned future provider behind
  the same seam.
- `INROAD_MASTER_KEY` is now the KEK that wraps the per-workspace DEKs.
  Operationally nothing changes: it is still a base64 encoding of 32 raw bytes
  (`openssl rand -base64 32`, must decode to 32 bytes). Losing it still loses
  every sealed secret (it now unwraps the DEKs), so treat it as the single most
  sensitive value in the deployment. Deleting a workspace destroys its DEK and
  permanently shreds that workspace's sealed data.

## Production notes
- Set strong INROAD_JWT_SECRET and INROAD_MASTER_KEY (see .env.example for generation).
- Leave INROAD_KEY_PROVIDER at its `local` default unless/until a cloud-KMS
  provider ships.
- Put a TLS-terminating reverse proxy in front of :8080.
- For worker fleets across multiple IPs, run the worker binary under systemd
  (templates in deploy/systemd/) rather than compose.

## Warm-up and the worker fleet
Warm-up is on once a workspace enables ≥2 mailboxes for it (via the app, or
`PUT /api/v1/mailboxes/{id}/warmup`); no server flag is required. A `warmup:sweep`
scheduler tick paces sends automatically. Recipient-side engagement (rescue-from-spam,
mark-read, reply) needs the connected mailbox to allow write access — IMAP `MOVE`/`STORE`
for SMTP mailboxes, the `gmail.modify` scope for Gmail; Microsoft 365 sends warm-up mail
but its recipient-side engagement is a documented follow-up.

- `INROAD_WARMUP_SECRET` — HMAC key for the `X-Inroad-Warmup` receipt token that lets a
  recipient mailbox recognize warm-up mail and isolate it from campaign handling. Optional:
  if unset it falls back to `INROAD_JWT_SECRET`. If you set it explicitly, use ≥16 bytes
  (`openssl rand -base64 32`) and keep it as stable as `INROAD_JWT_SECRET` — rotating it
  makes warm-up mail already in flight fall through to normal reply handling (harmless, but
  it skips that engagement).

Per-IP worker routing (optional — single-node deployments can ignore all of these and the
worker serves the shared default queue):

- `INROAD_WORKER_ID` — stable identity for this worker in the fleet registry. Defaults to
  the hostname. Give each worker a distinct id when running more than one.
- `INROAD_WORKER_EGRESS_IP` — the source IP this worker binds its outbound SMTP/IMAP dials
  to, so a mailbox's mail consistently leaves from one IP. Leave blank to use the OS default
  route. (It only sets the *source* address; the destination is still SSRF-vetted.)
- `INROAD_WORKER_QUEUES` — the asynq queues this worker consumes. Defaults to
  `w:<worker_id>,default`; a mailbox assigned to a worker routes to that worker's `w:<id>`
  queue, and everything unrouted falls to `default`.

## Connecting a Gmail mailbox (OAuth)

Inroad can connect Gmail / Google Workspace mailboxes via "Sign in with Google"
instead of an app password, and send / read replies through the Gmail API. This
is optional: leave the three `INROAD_GOOGLE_*` vars blank and Gmail OAuth stays
disabled — the connect-start endpoint returns `501 gmail oauth not configured`
and any pre-existing Gmail job fails cleanly (SMTP mailboxes are unaffected).

### 1. Create an OAuth client in Google Cloud Console
1. In the [Google Cloud Console](https://console.cloud.google.com/), pick (or
   create) a project.
2. **APIs & Services → Library →** enable the **Gmail API** for the project.
3. **APIs & Services → OAuth consent screen →** configure it (user type
   External unless everyone is inside one Workspace org). Add the scopes below.
4. **APIs & Services → Credentials → Create Credentials → OAuth client ID →**
   Application type **Web application**.
5. Under **Authorized redirect URIs**, add your redirect URI *verbatim* — it
   must exactly match `INROAD_GOOGLE_REDIRECT_URL` (see below), which defaults
   to `${INROAD_PUBLIC_URL}/oauth/google/callback`. For a deployment served at
   `https://inroad.example.com` that is:

        https://inroad.example.com/oauth/google/callback

6. Copy the generated **Client ID** and **Client secret**.

### 2. Scopes requested
Inroad requests exactly these OAuth scopes when connecting a Gmail mailbox — no
more:

- `https://www.googleapis.com/auth/gmail.send` — send outbound mail.
- `https://www.googleapis.com/auth/gmail.readonly` — poll replies and bounces.
- `openid`
- `email` — learn the connected mailbox's own address.

### 3. Set the environment variables
Put these in `.env` (all three are documented in `.env.example`):

        INROAD_GOOGLE_CLIENT_ID=<client id from step 1.6>
        INROAD_GOOGLE_CLIENT_SECRET=<client secret from step 1.6>
        # Optional. Defaults to ${INROAD_PUBLIC_URL}/oauth/google/callback.
        # Set it only if you need a redirect URI different from that default,
        # and it must match an Authorized redirect URI exactly.
        INROAD_GOOGLE_REDIRECT_URL=

`INROAD_PUBLIC_URL` must be the externally-reachable base URL of the API (it is
what the default redirect URI is built from), not `localhost`, in production.

### 4. Testing mode caveat (important)
Until you **publish and verify** the OAuth consent screen, the app stays in
Google's **"Testing"** mode. In testing mode Google **expires refresh tokens
after 7 days**, so a connected Gmail mailbox will stop sending about a week
after it is connected and must be reconnected. A real deployment must publish /
verify the OAuth consent screen (Google's app-verification process) before
relying on Gmail mailboxes.

## Connecting a Microsoft 365 mailbox (OAuth)

Inroad can connect Microsoft 365 / Exchange Online mailboxes via "Sign in with
Microsoft" instead of an app password, and send / read replies through the
Microsoft Graph API. This is optional: leave the `INROAD_MS_CLIENT_ID` /
`INROAD_MS_CLIENT_SECRET` vars blank and M365 OAuth stays disabled — the
connect-start endpoint returns `501 microsoft oauth not configured` and any
pre-existing M365 job fails cleanly (SMTP and Gmail mailboxes are unaffected).

### 1. Register an app in Microsoft Entra ID (Azure AD)
1. In the [Azure portal](https://portal.azure.com/), go to **Microsoft Entra ID
   → App registrations → New registration**.
2. Give it a name. For **Supported account types**, pick the option that matches
   your `INROAD_MS_TENANT` choice (see step 3): "Accounts in any organizational
   directory and personal Microsoft accounts" for the default `common`, or
   "Accounts in this organizational directory only" if you plan to pin a single
   tenant id.
3. Under **Redirect URI**, choose platform **Web** and add your redirect URI
   *verbatim* — it must exactly match `INROAD_MS_REDIRECT_URL` (see below), which
   defaults to `${INROAD_PUBLIC_URL}/oauth/microsoft/callback`. For a deployment
   served at `https://inroad.example.com` that is:

        https://inroad.example.com/oauth/microsoft/callback

4. Click **Register**. Copy the **Application (client) ID** from the overview page.
5. **Certificates & secrets → Client secrets → New client secret →** create one
   and copy its **Value** (not the Secret ID) immediately — it is shown only once.

### 2. API permissions requested
Under **API permissions → Add a permission → Microsoft Graph → Delegated
permissions**, add exactly these — no more:

- `Mail.Send` — send outbound mail.
- `Mail.Read` — poll replies and bounces.
- `User.Read` — learn the connected mailbox's own address.
- `offline_access` — issue a refresh token for control-plane token refresh.
- `openid`
- `email`

These are **delegated** (act as the signed-in user), not application
permissions, so no admin-consent-only application roles are involved. If your
tenant requires admin consent for delegated Graph scopes, an administrator can
grant it once from this same page.

### 3. Set the environment variables
Put these in `.env` (all four are documented in `.env.example`):

        INROAD_MS_CLIENT_ID=<Application (client) ID from step 1.4>
        INROAD_MS_CLIENT_SECRET=<client secret Value from step 1.5>
        # Optional. Defaults to ${INROAD_PUBLIC_URL}/oauth/microsoft/callback.
        # Set it only if you need a redirect URI different from that default,
        # and it must match a registered Web redirect URI exactly.
        INROAD_MS_REDIRECT_URL=
        # Optional. Azure AD authority; defaults to "common".
        INROAD_MS_TENANT=

`INROAD_PUBLIC_URL` must be the externally-reachable base URL of the API (it is
what the default redirect URI is built from), not `localhost`, in production.

### 4. Tenant scoping (`INROAD_MS_TENANT`)
`INROAD_MS_TENANT` defaults to `common`, meaning **any** Microsoft or Azure AD
account can consent and connect a mailbox. A self-hoster who wants to restrict
connections to their own organization should set `INROAD_MS_TENANT` to their
tenant id (the directory GUID, or a verified domain such as
`contoso.onmicrosoft.com`); Azure AD then rejects consent from outside that
tenant. Either way, every mailbox a callback creates is pinned to the workspace
of the signed state — tenant scoping only narrows *which Microsoft accounts* may
consent, it does not change Inroad's per-workspace isolation.
