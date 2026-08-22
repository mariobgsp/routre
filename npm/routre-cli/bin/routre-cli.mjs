#!/usr/bin/env node
// DEPRECATION STUB — routre-cli no longer distributes via npm.
//
// This shim no longer runs the binary (npm installs silently shipped stale
// platform binaries in the past). Install directly instead:
//
//   curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre-cli/main/install.sh | sh
//
// Existing npm installs keep working until uninstalled; the curl install
// replaces them cleanly.

console.error(
  "routre-cli is no longer distributed via npm.\n" +
    "Install the latest version with:\n\n" +
    "  curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre-cli/main/install.sh | sh\n" +
    "\nThen remove this npm copy:\n" +
    "  npm uninstall -g routre-cli\n",
);
process.exit(1);
