#!/usr/bin/env node
// routre-cli launcher: resolves the platform-specific binary from the
// matching optional-dependency package and spawns it with inherited stdio.
// The shim stays tiny on purpose — all logic lives in the Go binary.

import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const require = createRequire(import.meta.url);

function platformPkg() {
  const map = {
    "linux-x64": "routre-cli-linux-x64",
    "linux-arm64": "routre-cli-linux-arm64",
    "darwin-x64": "routre-cli-darwin-x64",
    "darwin-arm64": "routre-cli-darwin-arm64",
    "win32-x64": "routre-cli-win32-x64",
    "win32-arm64": "routre-cli-win32-arm64",
  };
  return map[`${process.platform}-${process.arch}`];
}

function binaryPath() {
  const pkg = platformPkg();
  if (pkg) {
    try {
      // Optional deps install into the package's own node_modules.
      return require.resolve(
        `${pkg}/binary/routre-cli${process.platform === "win32" ? ".exe" : ""}`,
      );
    } catch {
      /* fall through to dev binary */
    }
  }
  // Development layout: npm/routre-cli/binary/ holds the host-platform build.
  const dev = join(
    dirname(fileURLToPath(import.meta.url)),
    "..",
    "binary",
    `routre-cli${process.platform === "win32" ? ".exe" : ""}`,
  );
  return dev;
}

const bin = binaryPath();
const res = spawnSync(bin, process.argv.slice(2), {
  stdio: "inherit",
  env: process.env,
});

if (res.error) {
  console.error(
    `routre-cli: failed to run ${bin}\n` +
      "This usually means the platform package is missing — reinstall with:\n" +
      "  npm install -g routre-cli\n" +
      `(platform: ${process.platform}-${process.arch})`,
  );
  process.exit(1);
}
process.exit(res.status ?? 1);
