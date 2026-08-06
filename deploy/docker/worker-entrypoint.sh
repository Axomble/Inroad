#!/bin/sh
set -eu

# shellcheck disable=SC1091
. /usr/local/bin/load-secrets.sh

exec worker
