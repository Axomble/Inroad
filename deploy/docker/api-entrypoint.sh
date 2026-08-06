#!/bin/sh
set -eu

# shellcheck disable=SC1091
. /usr/local/bin/load-secrets.sh

migrate up
exec inroad
