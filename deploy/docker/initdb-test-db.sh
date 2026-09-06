#!/bin/sh
# Runs once, on first initialisation of the Postgres data volume.
#
# Integration tests connect to a SEPARATE database (inroad_test) so a test run
# never truncates the data you are developing against. Creating it here means
# `go test -tags=integration` works against the dev stack with no extra setup.
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
	CREATE DATABASE inroad_test OWNER $POSTGRES_USER;
EOSQL
