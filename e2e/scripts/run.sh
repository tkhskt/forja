#!/usr/bin/env bash
# Run the forja e2e suite against the connected (or freshly-created) emulator.
#
# Forces `-count=1` so Go's test cache never short-circuits us — e2e results
# depend on real device / emulator state that the cache cannot see, so a
# cached "PASS" can mask a regression that would actually fail today.
#
# Forwards every argument straight to `go test`, so common usage stays:
#
#   e2e/scripts/run.sh                          # full suite
#   e2e/scripts/run.sh -run TestCoreBasicRewrite  # one test
#   FORJA_E2E_KEEP=1 e2e/scripts/run.sh         # keep the emulator alive
#
# The whole suite (~33 tests, each booting/attaching against a real emulator)
# easily exceeds `go test`'s default 10m timeout on a cold-booted AVD — which
# manifests as a `panic: test timed out` mid-run, not a real failure. So we set
# a generous default timeout. Override with FORJA_E2E_TIMEOUT, or by passing
# your own `-timeout` (a later flag wins over this default).
#
# Run from anywhere — the script cd's to the e2e/ directory itself.
set -euo pipefail

cd "$(dirname "$0")/.."
exec go test -count=1 -tags e2e -timeout "${FORJA_E2E_TIMEOUT:-30m}" -v "$@" ./...
