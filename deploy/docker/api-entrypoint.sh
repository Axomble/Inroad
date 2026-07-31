#!/bin/sh
set -eu

migrate up
exec inroad
