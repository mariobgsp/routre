#!/usr/bin/env bash
# publish.sh — publishes routre-cli to the npm public registry.
#
# Order matters: the six platform binary packages MUST be published before
# the main "routre-cli" launcher (it references them via optionalDependencies).
#
# Auth: you must be logged in first:
#   npm login          # interactive; 2FA via TOTP
#   # or: export NPM_TOKEN=...  (granular access token, read+write)
#
# Then: ./npm/publish.sh
set -euo pipefail
cd "$(dirname "$0")/.."

# Ensure we have a fresh dist build.
export PATH="${HOME}/go-sdk/go/bin:${PATH}"
node npm/build.mjs

# Configure auth from NPM_TOKEN if provided (never echoed).
if [ -n "${NPM_TOKEN:-}" ]; then
  npm config set //registry.npmjs.org/:_authToken "$NPM_TOKEN"
fi
npm whoami >/dev/null || { echo "not logged in: run 'npm login' or set NPM_TOKEN" >&2; exit 1; }

cd npm/dist
for pkg in \
  routre-cli-linux-x64 routre-cli-linux-arm64 \
  routre-cli-darwin-x64 routre-cli-darwin-arm64 \
  routre-cli-win32-x64 routre-cli-win32-arm64; do
  echo "==> publishing $pkg"
  npm publish "$pkg"-0.1.0.tgz --access public
done
echo "==> publishing routre-cli"
npm publish routre-cli-0.1.0.tgz --access public

echo
echo "published. verify with: npm view routre-cli"
