#!/usr/bin/env bash
# Run the forja e2e suite across multiple Android API levels.
#
# The regular wrapper (run.sh) runs the suite once against whatever emulator is
# connected (or a single freshly-booted AVD). This matrix runner instead boots
# a *dedicated, owned* emulator per API level so it can verify forja's
# attach + bytecode-instrumentation behavior on older ART versions too.
#
# Default matrix: API 28 (forja's minSdk floor — the lowest level where
# `am attach-agent` is stable) plus the highest API level whose system image is
# installed locally. Override with FORJA_E2E_MATRIX_APIS.
#
# For each API the suite goes through its normal TestMain lifecycle (boot AVD,
# install fixtures, run, tear down), so the emulators are created and destroyed
# one at a time — only one device is ever connected while a run is in flight,
# which keeps the bare `adb` calls in the suite unambiguous.
#
# Env vars:
#   FORJA_E2E_MATRIX_APIS   Space-separated API levels to test
#                           (default: "28 <highest-installed>").
#   FORJA_E2E_MATRIX_FORCE  Set to skip the "no foreign device connected" guard
#                           (use only if you've exported ANDROID_SERIAL yourself).
#   FORJA_E2E_ABI / _TAG    Forwarded to setup_emulator.sh (image selection).
#
# Every other argument is forwarded to `go test`, e.g.:
#   e2e/scripts/run_matrix.sh -run TestCore
#   FORJA_E2E_MATRIX_APIS="28 31 34" e2e/scripts/run_matrix.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
E2E_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Locate the SDK (same precedence as setup_emulator.sh) -----------------

ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
if [ -z "$ANDROID_SDK_ROOT" ]; then
    for candidate in \
        "$HOME/Library/Android/sdk" \
        "$HOME/Android/Sdk" \
        "/opt/android-sdk" \
        "/usr/local/share/android-sdk"
    do
        [ -d "$candidate" ] && ANDROID_SDK_ROOT="$candidate" && break
    done
fi
if [ -z "$ANDROID_SDK_ROOT" ]; then
    echo "ERROR: could not find Android SDK (set ANDROID_HOME)." >&2
    exit 2
fi

# adb on PATH or fall back to platform-tools.
if ! command -v adb >/dev/null 2>&1; then
    if [ -x "$ANDROID_SDK_ROOT/platform-tools/adb" ]; then
        export PATH="$ANDROID_SDK_ROOT/platform-tools:$PATH"
    else
        echo "ERROR: adb not found." >&2
        exit 2
    fi
fi

# --- Resolve the default matrix --------------------------------------------

# Host-default ABI (overridable), used only to find the highest installed image.
ABI="${FORJA_E2E_ABI:-}"
if [ -z "$ABI" ]; then
    case "$(uname -m)" in
        arm64|aarch64) ABI="arm64-v8a" ;;
        *)             ABI="x86_64" ;;
    esac
fi

highest_installed_api() {
    local sys_img_dir="$ANDROID_SDK_ROOT/system-images"
    [ -d "$sys_img_dir" ] || return 1
    local best=""
    for api_dir in "$sys_img_dir"/android-*; do
        [ -d "$api_dir" ] || continue
        local api="${api_dir##*/android-}"
        # Only count it if some tag carries our ABI.
        local has_abi=""
        for tag_dir in "$api_dir"/*; do
            [ -d "$tag_dir/$ABI" ] && has_abi=1 && break
        done
        [ -n "$has_abi" ] || continue
        case "$api" in (*[!0-9]*) continue ;; esac
        if [ -z "$best" ] || [ "$api" -gt "$best" ]; then
            best="$api"
        fi
    done
    [ -n "$best" ] && printf '%s\n' "$best"
}

if [ -n "${FORJA_E2E_MATRIX_APIS:-}" ]; then
    APIS="$FORJA_E2E_MATRIX_APIS"
else
    latest="$(highest_installed_api || true)"
    # No image installed yet → setup_emulator.sh will sdkmanager-install 34.
    [ -z "$latest" ] && latest=34
    if [ "$latest" = "28" ]; then
        APIS="28"
    else
        APIS="28 $latest"
    fi
fi

# --- Guard: a foreign device would make bare `adb` ambiguous ---------------

if [ -z "${FORJA_E2E_MATRIX_FORCE:-}" ]; then
    if adb devices | awk 'NR>1 && $2=="device" {found=1} END{exit !found}'; then
        echo "ERROR: a device/emulator is already connected:" >&2
        adb devices | awk 'NR>1 && $2=="device" {print "  "$0}' >&2
        echo >&2
        echo "The matrix runner boots its own emulator per API level, so it" >&2
        echo "needs a clean slate. Stop the running emulator(s) first, or set" >&2
        echo "FORJA_E2E_MATRIX_FORCE=1 if you've exported ANDROID_SERIAL yourself." >&2
        exit 2
    fi
fi

# Tearing down between APIs is mandatory — keeping an emulator alive would
# leave two devices connected for the next run. Forbid FORJA_E2E_KEEP here.
if [ -n "${FORJA_E2E_KEEP:-}" ]; then
    echo "[matrix] ignoring FORJA_E2E_KEEP (each API's emulator must be torn down)" >&2
    unset FORJA_E2E_KEEP
fi

echo "[matrix] API levels: $APIS (abi=$ABI)" >&2

# Wait until no device is connected (the suite tears its emulator down at the
# end of each run; this confirms it's fully gone before the next API boots, so
# we never have two devices fighting over bare `adb`).
wait_no_devices() {
    for _ in $(seq 1 30); do
        if ! adb devices | awk 'NR>1 && $2=="device" {found=1} END{exit !found}'; then
            return 0
        fi
        sleep 1
    done
    echo "[matrix] WARNING: a device is still connected after teardown" >&2
}

# --- Run the suite once per API --------------------------------------------

declare -a RESULTS=()
overall=0
for api in $APIS; do
    echo >&2
    echo "==================================================================" >&2
    echo "[matrix] === API $api ===" >&2
    echo "==================================================================" >&2
    status_file="${TMPDIR:-/tmp}/forja-e2e-api${api}.status"
    if FORJA_E2E_NO_BORROW=1 \
       FORJA_E2E_API="$api" \
       FORJA_E2E_AVD="forja-e2e-api${api}" \
       FORJA_E2E_STATUS_FILE="$status_file" \
       "$SCRIPT_DIR/run.sh" "$@"; then
        RESULTS+=("API $api: PASS")
    else
        RESULTS+=("API $api: FAIL")
        overall=1
    fi
    # TestMain tears its own emulator down on the happy path, but a Go test
    # timeout `panic` bypasses that — explicitly tear down (idempotent: a no-op
    # once the status file is gone) so a leaked emulator can't bleed into the
    # next API's run.
    FORJA_E2E_STATUS_FILE="$status_file" "$SCRIPT_DIR/teardown_emulator.sh" || true
    wait_no_devices
done

echo >&2
echo "==================== matrix summary ====================" >&2
for r in "${RESULTS[@]}"; do
    echo "  $r" >&2
done
echo "========================================================" >&2

exit "$overall"
