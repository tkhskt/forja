#!/usr/bin/env bash
# Boot or reuse an Android emulator for forja's e2e suite.
#
# Behavior:
#   1. If an emulator is already connected via adb, reuse it (= fastest local
#      dev path: keep your AS emulator running and just run the tests).
#   2. Otherwise, find an installed system image, create an AVD if missing,
#      and start the emulator headless.
#
# Env vars (all optional):
#   FORJA_E2E_AVD          AVD name to create / reuse (default: forja-e2e)
#   FORJA_E2E_API          Force a specific API level (default: auto-pick highest available)
#   FORJA_E2E_ABI          Force a specific ABI (default: auto-pick from host arch)
#   FORJA_E2E_TAG          Force image tag, e.g. google_apis / google_apis_playstore
#                          (default: auto-pick from what's installed)
#   FORJA_E2E_STATUS_FILE  Path for the owner-vs-borrow status file
#                          (default: ${TMPDIR:-/tmp}/forja-e2e-emulator.status)

set -euo pipefail

AVD_NAME="${FORJA_E2E_AVD:-forja-e2e}"
STATUS_FILE="${FORJA_E2E_STATUS_FILE:-${TMPDIR:-/tmp}/forja-e2e-emulator.status}"

# --- Locate the SDK ---------------------------------------------------------

ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
if [ -z "$ANDROID_SDK_ROOT" ]; then
    for candidate in \
        "$HOME/Library/Android/sdk" \
        "$HOME/Android/Sdk" \
        "/opt/android-sdk" \
        "/usr/local/share/android-sdk"
    do
        if [ -d "$candidate" ]; then
            ANDROID_SDK_ROOT="$candidate"
            echo "[setup] auto-detected ANDROID_SDK_ROOT=$candidate" >&2
            export ANDROID_SDK_ROOT
            export ANDROID_HOME="$candidate"
            break
        fi
    done
fi
if [ -z "$ANDROID_SDK_ROOT" ]; then
    echo "ERROR: could not find Android SDK." >&2
    echo "       Set ANDROID_HOME or install Android Studio (which places the" >&2
    echo "       SDK under ~/Library/Android/sdk on macOS, ~/Android/Sdk on Linux)." >&2
    exit 2
fi

# --- Locate SDK tools (prefer cmdline-tools/latest/, fall back to tools/bin/)

find_tool() {
    local name="$1"
    for path in \
        "$ANDROID_SDK_ROOT/cmdline-tools/latest/bin/$name" \
        "$ANDROID_SDK_ROOT/cmdline-tools/bin/$name" \
        "$ANDROID_SDK_ROOT/tools/bin/$name"
    do
        if [ -x "$path" ]; then
            echo "$path"
            return 0
        fi
    done
    return 1
}

AVDMANAGER="$(find_tool avdmanager || true)"
SDKMANAGER="$(find_tool sdkmanager || true)"
EMULATOR_BIN="$ANDROID_SDK_ROOT/emulator/emulator"

if [ -z "$AVDMANAGER" ] || [ ! -x "$EMULATOR_BIN" ]; then
    echo "ERROR: missing required SDK tool(s)." >&2
    echo "  avdmanager: ${AVDMANAGER:-NOT FOUND}" >&2
    echo "  emulator:   $EMULATOR_BIN $([ -x "$EMULATOR_BIN" ] && echo OK || echo NOT FOUND)" >&2
    echo "  Install via Android Studio's SDK Manager (Tools → SDK Manager → SDK Tools)" >&2
    echo "  or run: sdkmanager 'cmdline-tools;latest'" >&2
    exit 2
fi

# --- adb: use platform-tools/adb if PATH doesn't have it --------------------

if ! command -v adb >/dev/null 2>&1; then
    if [ -x "$ANDROID_SDK_ROOT/platform-tools/adb" ]; then
        export PATH="$ANDROID_SDK_ROOT/platform-tools:$PATH"
    else
        echo "ERROR: adb not found in PATH and not under $ANDROID_SDK_ROOT/platform-tools" >&2
        exit 2
    fi
fi

# --- Already-running device? Reuse it. -------------------------------------

if adb devices | awk 'NR>1 && $2=="device" {found=1} END{exit !found}'; then
    echo "[setup] re-using already-connected device:"
    adb devices | awk 'NR>1 && $2=="device" {print "  "$0}'
    printf '{"owned":false}\n' > "$STATUS_FILE"
    exit 0
fi

# --- Pick a system image -----------------------------------------------------

# Auto-detect default ABI from host architecture, unless overridden.
if [ -z "${FORJA_E2E_ABI:-}" ]; then
    case "$(uname -m)" in
        arm64|aarch64) FORJA_E2E_ABI="arm64-v8a" ;;
        *)             FORJA_E2E_ABI="x86_64" ;;
    esac
fi
ABI="$FORJA_E2E_ABI"

# Find the installed system image that best matches the user's requests.
# Iterate over system-images/android-<api>/<tag>/<abi>/ directories.
SYS_IMG_DIR="$ANDROID_SDK_ROOT/system-images"

pick_installed_image() {
    if [ ! -d "$SYS_IMG_DIR" ]; then
        return 1
    fi
    local want_api="${FORJA_E2E_API:-}"
    local want_tag="${FORJA_E2E_TAG:-}"
    local best_api="" best_tag="" best_dir=""

    # Tag preference: google_apis is lighter and forja doesn't need play
    # services; fall back to anything else only if it's all we have.
    for tag_pref in "$want_tag" google_apis google_apis_playstore default android-wear; do
        [ -z "$tag_pref" ] && continue
        for api_dir in "$SYS_IMG_DIR"/android-*; do
            [ -d "$api_dir" ] || continue
            local api="${api_dir##*/android-}"
            [ -n "$want_api" ] && [ "$api" != "$want_api" ] && continue
            local tag_dir="$api_dir/$tag_pref"
            [ -d "$tag_dir/$ABI" ] || continue
            # Compare numeric API levels — pick highest.
            if [ -z "$best_api" ] || [ "$api" -gt "$best_api" ]; then
                best_api="$api"
                best_tag="$tag_pref"
                best_dir="$tag_dir/$ABI"
            fi
        done
        if [ -n "$best_dir" ]; then
            printf '%s %s %s\n' "$best_api" "$best_tag" "$best_dir"
            return 0
        fi
    done
    return 1
}

if pick_result="$(pick_installed_image)"; then
    read -r API_LEVEL TAG _ <<< "$pick_result"
    echo "[setup] using installed system image: android-$API_LEVEL $TAG $ABI" >&2
else
    # Nothing installed for our criteria — try sdkmanager install.
    API_LEVEL="${FORJA_E2E_API:-34}"
    TAG="${FORJA_E2E_TAG:-google_apis}"
    SYSTEM_IMAGE="system-images;android-${API_LEVEL};${TAG};${ABI}"
    if [ -z "$SDKMANAGER" ]; then
        echo "ERROR: no installed system image matches (api=${FORJA_E2E_API:-any} tag=${FORJA_E2E_TAG:-any} abi=$ABI)," >&2
        echo "       and sdkmanager isn't available to install one." >&2
        echo "       Available under $SYS_IMG_DIR:" >&2
        ls "$SYS_IMG_DIR" 2>/dev/null | sed 's/^/         /' >&2 || true
        exit 2
    fi
    echo "[setup] installing system image: $SYSTEM_IMAGE ..." >&2
    yes | "$SDKMANAGER" --install "$SYSTEM_IMAGE" >/dev/null
fi

SYSTEM_IMAGE="system-images;android-${API_LEVEL};${TAG};${ABI}"

# --- Create AVD if it doesn't already exist --------------------------------

if ! "$AVDMANAGER" list avd 2>/dev/null | grep -q "Name: ${AVD_NAME}$"; then
    echo "[setup] creating AVD: $AVD_NAME (api=$API_LEVEL tag=$TAG abi=$ABI)" >&2
    echo "no" | "$AVDMANAGER" create avd \
        --name "$AVD_NAME" \
        --package "$SYSTEM_IMAGE" \
        --device "pixel" \
        --force >/dev/null
fi

# --- Boot emulator ---------------------------------------------------------

echo "[setup] starting emulator $AVD_NAME ..." >&2
"$EMULATOR_BIN" -avd "$AVD_NAME" \
    -no-window -no-audio -no-boot-anim \
    -no-snapshot-save \
    -wipe-data \
    >"${TMPDIR:-/tmp}/forja-e2e-emulator.log" 2>&1 &
EMULATOR_PID=$!
echo "[setup] emulator PID=$EMULATOR_PID (log: ${TMPDIR:-/tmp}/forja-e2e-emulator.log)" >&2

echo "[setup] waiting for adb ..." >&2
adb wait-for-device

echo "[setup] waiting for sys.boot_completed=1 (up to 180s) ..." >&2
completed=""
for i in $(seq 1 90); do
    completed=$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r' || true)
    if [ "$completed" = "1" ]; then
        break
    fi
    sleep 2
done
if [ "$completed" != "1" ]; then
    echo "ERROR: emulator failed to boot within 180s" >&2
    kill "$EMULATOR_PID" 2>/dev/null || true
    exit 1
fi

# Reduce flakiness from animations / lock screen.
adb shell settings put global window_animation_scale 0 >/dev/null 2>&1 || true
adb shell settings put global transition_animation_scale 0 >/dev/null 2>&1 || true
adb shell settings put global animator_duration_scale 0 >/dev/null 2>&1 || true

printf '{"owned":true,"pid":%s,"avd":"%s"}\n' "$EMULATOR_PID" "$AVD_NAME" > "$STATUS_FILE"
echo "[setup] ready (status: $STATUS_FILE)" >&2
