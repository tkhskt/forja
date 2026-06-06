#!/usr/bin/env bash
# License audit driver — produces reports for both the Gradle (Kotlin/Android)
# and Go halves of forja, and fails fast on any unexpected license.
#
# Usage:
#   scripts/check-licenses.sh
#
# Reports land under:
#   runtime/build/reports/dependency-license/
#   jvmti-agent/build/reports/dependency-license/
#   cli/build/license-report.csv
#
# Requires:
#   - Gradle wrapper (./gradlew) — bundles jk1's license-report plugin via
#     scripts/license-report.init.gradle.kts
#   - go-licenses (install with: go install github.com/google/go-licenses@latest)
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Gradle (Kotlin / Android) ==="
./gradlew --init-script scripts/license-report.init.gradle.kts \
    :runtime:checkLicense :runtime:generateLicenseReport \
    :jvmti-agent:checkLicense :jvmti-agent:generateLicenseReport

echo
echo "=== Go (forja CLI) ==="
GOLIC=$(go env GOPATH)/bin/go-licenses
if [[ ! -x "$GOLIC" ]]; then
    echo "go-licenses not installed. Install with:"
    echo "    go install github.com/google/go-licenses@latest"
    exit 1
fi

mkdir -p cli/build
# go-localereader v0.0.1 ships without a LICENSE file in its module cache
# (upstream has one; the v0.0.1 tag predates its addition). We've vendored
# the verbatim MIT text into NOTICE section 6, so tell go-licenses to skip
# it instead of failing the run with "Failed to find license".
GOLIC_IGNORE="github.com/mattn/go-localereader"
(
    cd cli
    "$GOLIC" check ./... \
        --disallowed_types=forbidden,restricted \
        --ignore="$GOLIC_IGNORE"
    "$GOLIC" report ./... --ignore="$GOLIC_IGNORE" > build/license-report.csv
)

echo
echo "All license checks passed."
echo "Inspect HTML reports at:"
echo "  runtime/build/reports/dependency-license/index.html"
echo "  jvmti-agent/build/reports/dependency-license/index.html"
echo "  cli/build/license-report.csv"
