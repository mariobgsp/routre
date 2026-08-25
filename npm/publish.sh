#!/usr/bin/env bash
# publish.sh — publishes routre to the npm public registry.
#
# Order matters: the six platform binary packages MUST be published before
# the main "routre" launcher (it references them via optionalDependencies).
#
# Auth: you must be logged in first:
#   npm login          # interactive; 2FA via TOTP
#   # or: export NPM_TOKEN=...  (granular access token, read+write)
#
# Then: ./npm/publish.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.1.2}"

# Ensure we have a fresh dist build.
export PATH="${HOME}/go-sdk/go/bin:${PATH}"
node npm/build.mjs

# Configure auth from NPM_TOKEN if provided (never echoed).
if [ -n "${NPM_TOKEN:-}" ]; then
  npm config set //registry.npmjs.org/:_authToken "$NPM_TOKEN"
fi
npm whoami >/dev/null || {
  echo "not logged in: run 'npm login' or set NPM_TOKEN" >&2
  exit 1
}

cd npm/dist
# Unscoped platform packages (Unix), then the scoped Windows packages
# (npm's spam detection rejects unscoped win32 names, so those ship under
# the @mariobgsp scope), then the launcher last.
for pkg in \
  routre-linux-x64 routre-linux-arm64 \
  routre-darwin-x64 routre-darwin-arm64; do
  echo "==> publishing $pkg"
  npm publish "$pkg"-"$VERSION".tgz --access public
done
for pkg in \
  mariobgsp-routre-win32-x64 mariobgsp-routre-win32-arm64; do
  echo "==> publishing @$pkg"
  npm publish "$pkg"-"$VERSION".tgz --access public
done
echo "==> publishing routre"
npm publish routre-"$VERSION".tgz --access public

echo
echo "published. verify with: npm view routre"
