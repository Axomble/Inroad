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

## Production notes
- Set strong INROAD_JWT_SECRET and INROAD_MASTER_KEY (see .env.example for generation).
- Put a TLS-terminating reverse proxy in front of :8080.
- For worker fleets across multiple IPs, run the worker binary under systemd
  (templates in deploy/systemd/) rather than compose.

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
