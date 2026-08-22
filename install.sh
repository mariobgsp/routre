#!/bin/sh
# routre-cli installer.
#
#   curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre-cli/main/install.sh | sh
#
# Or download first, inspect, then run:
#   sh install.sh
#
# Env overrides:
#   ROUTRE_INSTALL_DIR   target dir (default ~/.local/bin; never uses sudo)
#   ROUTRE_VERSION       release tag (default: latest release)
#
# Never calls api.github.com (60 req/h/IP limit): every URL is a
# releases/latest/download or releases/download redirect.

set -eu

REPO="${ROUTRE_REPO:-mariobgsp/routre-cli}"
PREFIX="${ROUTRE_INSTALL_DIR:-$HOME/.local/bin}"
BASE="https://github.com/${REPO}"

log() { printf '%s\n' "$*" >&2; }
die() { log "install.sh: $*"; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# --- fetch helper: curl or wget -------------------------------------------
fetch() { # fetch <url> <outfile|->
    if have curl; then
        if [ "$2" = "-" ]; then curl -fsSL "$1"; else curl -fsSL -o "$2" "$1"; fi
    elif have wget; then
        if [ "$2" = "-" ]; then wget -qO- "$1"; else wget -qO "$2" "$1"; fi
    else
        die "need curl or wget to download files"
    fi
}

TMP=""
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT INT TERM

# --- platform detection ----------------------------------------------------
os=$(uname -s)
arch=$(uname -m)

case "$os" in
    Linux*) goos=linux ;;
    Darwin*) goos=darwin ;;
    MINGW* | MSYS* | CYGWIN*)
        log "Windows detected: this installer covers macOS/Linux."
        log "Download manually instead:"
        log "  ${BASE}/releases/latest/download/routre-cli_windows_amd64.zip"
        exit 1
        ;;
    *) die "unsupported operating system: $os" ;;
esac

case "$arch" in
    x86_64 | amd64) goarch=amd64 ;;
    aarch64 | arm64) goarch=arm64 ;;
    *) die "unsupported architecture: $arch" ;;
esac

asset="routre-cli_${goos}_${goarch}.tar.gz"

# --- download ---------------------------------------------------------------
TMP=$(mktemp -d)
log "downloading ${asset}..."
fetch "${BASE}/releases/latest/download/${asset}" "${TMP}/${asset}"
fetch "${BASE}/releases/latest/download/checksums.txt" "${TMP}/checksums.txt"

# --- verify sha256 ----------------------------------------------------------
want=$(grep " ${asset}\$" "${TMP}/checksums.txt" | awk '{print $1}')
[ -n "$want" ] || die "no checksum found for ${asset} — aborting"
if have sha256sum; then
    got=$(sha256sum "${TMP}/${asset}" | awk '{print $1}')
elif have shasum; then
    got=$(shasum -a 256 "${TMP}/${asset}" | awk '{print $1}')
else
    die "need sha256sum or shasum to verify the download"
fi
[ "$got" = "$want" ] || die "checksum mismatch for ${asset}
  want $want
  got  $got
(the download may be truncated or tampered — nothing was installed)"
log "checksum OK"

# --- extract + install ------------------------------------------------------
tar czf /dev/null "${TMP}/${asset}" 2>/dev/null || true # sanity: readable gzip
tar xzf "${TMP}/${asset}" -C "$TMP"

mkdir -p "$PREFIX"
if [ -w "$PREFIX" ]; then
    mv "$TMP/routre-cli" "${PREFIX}/routre-cli"
else
    die "cannot write to ${PREFIX} (set ROUTRE_INSTALL_DIR elsewhere)"
fi
chmod 0755 "${PREFIX}/routre-cli"

case ":${PATH}:" in
    *":${PREFIX}:"*) ;;
    *)
        log
        log "NOTE: ${PREFIX} is not on your PATH. Add this to your shell profile:"
        log "  export PATH=\"${PREFIX}:\$PATH\""
        ;;
esac

installed_version=$("${PREFIX}/routre-cli" version 2>/dev/null || echo "(unknown)")
log
log "installed: ${PREFIX}/routre-cli (${installed_version})"
log "run 'routre-cli update' later to upgrade."
