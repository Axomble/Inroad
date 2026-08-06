# Sourced (not executed) by api-entrypoint.sh, worker-entrypoint.sh, and the
# migrate service's command, before the real process starts.
#
# Populates INROAD_JWT_SECRET / INROAD_MASTER_KEY from the file generate-secrets.sh
# produced, but ONLY when they are not already set — an operator-supplied value
# (Kubernetes secret, ECS task definition, a real KMS-backed deploy) always
# wins over the zero-config fallback file.
if [ -z "${INROAD_JWT_SECRET:-}" ] || [ -z "${INROAD_MASTER_KEY:-}" ]; then
    if [ -f "${SECRETS_FILE:-/run/secrets/inroad/env}" ]; then
        # shellcheck disable=SC1090,SC1091
        . "${SECRETS_FILE:-/run/secrets/inroad/env}"
    fi
fi
