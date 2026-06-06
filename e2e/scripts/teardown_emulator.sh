#!/usr/bin/env bash
# Counterpart to setup_emulator.sh — kills the emulator only if WE started it.
# Borrowed emulators (= user's own AS-launched device) are left running.

set -euo pipefail

STATUS_FILE="${FORJA_E2E_STATUS_FILE:-${TMPDIR:-/tmp}/forja-e2e-emulator.status}"

if [ ! -f "$STATUS_FILE" ]; then
    # Nothing recorded → nothing to do.
    exit 0
fi

owned=$(grep -o '"owned":[^,}]*' "$STATUS_FILE" | head -1 | cut -d: -f2 | tr -d ' ')
if [ "$owned" != "true" ]; then
    echo "[teardown] emulator was borrowed, leaving it running"
    rm -f "$STATUS_FILE"
    exit 0
fi

pid=$(grep -o '"pid":[0-9]*' "$STATUS_FILE" | head -1 | cut -d: -f2)
echo "[teardown] stopping emulator (pid=$pid) ..."

# Try the clean way first; force-kill if that doesn't take.
adb emu kill 2>/dev/null || true
sleep 2
if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    sleep 1
    kill -KILL "$pid" 2>/dev/null || true
fi

rm -f "$STATUS_FILE"
echo "[teardown] done"
