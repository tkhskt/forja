#!/usr/bin/env bash
# forja installer — one-liner for picking up the latest pre-built release.
#
# Same command for fresh install AND update — re-running this overwrites the
# binary and wipes-then-recopies the agent bundle so stale agent files from a
# previous version never linger.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
#
# Env overrides:
#   FORJA_VERSION   tag to install (default: latest)
#   PREFIX          base install dir (default: $HOME/.local)
#
# Installs:
#   $PREFIX/bin/forja
#   $PREFIX/share/forja/agent/{agent-bundle.dex, libforja-agent-<abi>.so}

set -euo pipefail

REPO="tkhskt/forja"
PREFIX="${PREFIX:-$HOME/.local}"
VERSION="${FORJA_VERSION:-latest}"

# ---- OS / arch detection ----
case "$(uname -s)" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *)      echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *)             echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

# ---- Resolve the release tag (latest → real version) ----
resolve_latest() {
    local api="https://api.github.com/repos/${REPO}/releases/latest"
    # Materialize curl's output into a var first. Piping `curl | grep -m1`
    # directly is fragile under `set -o pipefail`: grep closes its stdin as
    # soon as it matches, which can drop a SIGPIPE on curl mid-write
    # (depending on pipe buffer size, curl version, and timing). That
    # SIGPIPE'd curl returns non-zero, pipefail propagates it, set -e exits
    # the script silently right after VERSION has already been assigned —
    # exactly the symptom we kept seeing on some macs.
    local resp
    resp="$(curl -fsSL "$api")"
    # Use a tiny grep instead of jq so the script has zero runtime deps.
    printf '%s' "$resp" \
        | grep -m1 '"tag_name":' \
        | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/'
}
if [[ "$VERSION" == "latest" ]]; then
    VERSION="$(resolve_latest)"
    if [[ -z "$VERSION" ]]; then
        echo "could not resolve latest release tag from GitHub API" >&2
        exit 1
    fi
fi

# ---- Download + verify ----
ASSET="forja_${VERSION#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"

echo "==> Downloading ${ASSET}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT
curl -fsSL "$URL" -o "$TMPDIR/forja.tar.gz"

# ---- Extract into PREFIX ----
echo "==> Installing into $PREFIX"
mkdir -p "$PREFIX/bin"
tar -xzf "$TMPDIR/forja.tar.gz" -C "$TMPDIR"

# Expected layout inside tarball:
#   forja_<ver>_<os>_<arch>/
#     bin/forja
#     share/forja/agent/agent-bundle.dex
#     share/forja/agent/libforja-agent-*.so
src="$TMPDIR/forja_${VERSION#v}_${OS}_${ARCH}"

# `install` overwrites the binary in place. The agent dir is wiped first so
# .so files dropped in a future release don't linger from a previous install.
install -m 0755 "$src/bin/forja" "$PREFIX/bin/forja"
rm -rf "$PREFIX/share/forja/agent"
mkdir -p "$PREFIX/share/forja/agent"
cp "$src/share/forja/agent/"* "$PREFIX/share/forja/agent/"

# ---- Finish ----
echo
echo "Installed forja ${VERSION}"
echo "(re-run the same command later to update — agent files are refreshed cleanly)"
echo
if ! command -v forja >/dev/null 2>&1 || [[ "$(command -v forja)" != "$PREFIX/bin/forja" ]]; then
    echo "Add ${PREFIX}/bin to PATH if it isn't already:"
    echo "    export PATH=\"$PREFIX/bin:\$PATH\""
    echo
fi
echo "Verify with: forja --help"
