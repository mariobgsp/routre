#!/usr/bin/env node
// DEPRECATION STUB — routre no longer distributes via npm.
//
// This shim no longer runs the binary. Install directly instead:
//
//   curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre/main/install.sh | sh
//
// Existing npm installs keep working until uninstalled.

console.error(
  "routre is no longer distributed via npm.\n" +
    "Install the latest version with:\n\n" +
    "  curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre/main/install.sh | sh\n" +
    "\nThen remove this npm copy:\n" +
    "  npm uninstall -g routre\n",
);
process.exit(1);
