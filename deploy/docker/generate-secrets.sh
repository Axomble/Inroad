#!/bin/sh
# Runs once, before postgres/api/worker start. Generates INROAD_JWT_SECRET and
# INROAD_MASTER_KEY into a file on a shared volume the first time the stack
# boots, so `docker compose up` with no configuration stays zero-config
# WITHOUT falling back to a fixed, publicly-known value baked into the image
# or this repo. On every subsequent boot the file already exists and is left
# untouched, so the secrets (and therefore every previously-sealed DEK) are
# stable across restarts.
#
# An operator who sets INROAD_JWT_SECRET / INROAD_MASTER_KEY explicitly (a
# real deployment wiring in a KMS, Kubernetes secret, or ECS task definition)
# is unaffected — see load-secrets.sh, which only consults this file when
# those variables are not already present in the environment.
set -eu

out_dir=$(dirname "$SECRETS_FILE")
mkdir -p "$out_dir"

if [ -f "$SECRETS_FILE" ]; then
    echo "generate-secrets: $SECRETS_FILE already exists, leaving it alone"
    exit 0
fi

umask 077
{
    # >= 16 bytes required; 48 random bytes base64-encoded is comfortably over.
    printf 'export INROAD_JWT_SECRET=%s\n' "$(head -c 48 /dev/urandom | base64 | tr -d '\n')"
    # Must decode to EXACTLY 32 bytes (AES-256), so encode exactly 32 raw bytes.
    printf 'export INROAD_MASTER_KEY=%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
} > "$SECRETS_FILE"

# Readable by the non-root `inroad` user (uid 10001) the api/worker/migrate
# containers run as. This volume is not exposed outside the compose network,
# so world-readable-within-the-stack is an acceptable trade for not having to
# match uids across three separately-built images.
chmod 0644 "$SECRETS_FILE"
echo "generate-secrets: wrote a fresh $SECRETS_FILE"
