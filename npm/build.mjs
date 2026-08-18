#!/usr/bin/env node
// build.mjs — compiles the Go binary for all six platforms into
// npm/routre-cli-<os>-<arch>/binary/, writes each platform package.json,
// and npm-packs everything into npm/dist.
//
// Prerequisites: Go toolchain on PATH, npm available.
// Run: node npm/build.mjs  (or `make dist-npm`)

import { execSync } from "node:child_process";
import {
  mkdirSync,
  writeFileSync,
  rmSync,
  chmodSync,
  copyFileSync,
  existsSync,
} from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const npmDir = join(root, "npm");
const distDir = join(npmDir, "dist");
const version = "0.1.6";

const targets = [
  {
    pkg: "routre-cli-linux-x64",
    os: "linux",
    goos: "linux",
    arch: "amd64",
    ext: "",
  },
  {
    pkg: "routre-cli-linux-arm64",
    os: "linux",
    goos: "linux",
    arch: "arm64",
    ext: "",
  },
  {
    pkg: "routre-cli-darwin-x64",
    os: "darwin",
    goos: "darwin",
    arch: "amd64",
    ext: "",
  },
  {
    pkg: "routre-cli-darwin-arm64",
    os: "darwin",
    goos: "darwin",
    arch: "arm64",
    ext: "",
  },
  {
    // Scoped: npm's spam detection rejects unscoped `routre-cli-win32-*`
    // names, so the Windows packages ship under the @mariobgsp scope.
    pkg: "@mariobgsp/routre-cli-win32-x64",
    os: "win32",
    goos: "windows",
    arch: "amd64",
    ext: ".exe",
  },
  {
    pkg: "@mariobgsp/routre-cli-win32-arm64",
    os: "win32",
    goos: "windows",
    arch: "arm64",
    ext: ".exe",
  },
];

rmSync(distDir, { recursive: true, force: true });
mkdirSync(distDir, { recursive: true });

const host = `${process.platform}-${process.arch}`;
const hostTarget =
  targets.find(
    (t) =>
      `${t.os}-${t.arch}` === host ||
      (process.platform === "win32" &&
        t.os === "win32" &&
        `${t.arch}` === process.arch),
  ) ?? targets.find((t) => t.os === process.platform);

function run(cmd, opts) {
  try {
    execSync(cmd, opts);
  } catch (err) {
    console.error(
      `build.mjs: command failed: ${cmd}\n${err?.stderr?.toString?.() ?? err}`,
    );
    process.exit(1);
  }
}

for (const t of targets) {
  const dir = join(npmDir, t.pkg);
  const binDir = join(dir, "binary");
  rmSync(binDir, { recursive: true, force: true });
  mkdirSync(binDir, { recursive: true });
  const out = join(binDir, `routre-cli${t.ext}`);

  console.log(`building ${t.pkg} (${t.goos}/${t.arch})...`);
  run(`go build -trimpath -ldflags "-s -w" -o "${out}" .`, {
    cwd: root,
    stdio: "inherit",
    env: { ...process.env, GOOS: t.goos, GOARCH: t.arch, CGO_ENABLED: "0" },
  });
  if (t.os !== "win32") chmodSync(out, 0o755);

  // Ship benchdata next to the binary so `routre-cli bench` works from any
  // cwd after a global install.
  const bdSrc = join(root, "benchdata");
  if (existsSync(bdSrc)) {
    run(`cp -r "${bdSrc}" "${binDir}/"`, { cwd: root });
  }

  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify(
      {
        name: t.pkg,
        version,
        description: `routre-cli binary for ${t.os}-${t.arch}`,
        license: "MIT",
        os: [t.os],
        cpu: [t.arch === "amd64" ? "x64" : t.arch],
        files: ["binary"],
      },
      null,
      2,
    ) + "\n",
  );
}

// Host-platform binary also lands in npm/routre-cli/binary/ so the shim
// works from a git checkout without npm install.
if (hostTarget) {
  const devDir = join(npmDir, "routre-cli", "binary");
  mkdirSync(devDir, { recursive: true });
  const src = join(
    npmDir,
    hostTarget.pkg,
    "binary",
    `routre-cli${hostTarget.ext}`,
  );
  const dst = join(devDir, `routre-cli${hostTarget.ext}`);
  copyFileSync(src, dst);
  if (hostTarget.os !== "win32") chmodSync(dst, 0o755);
  console.log(
    `host binary copied to npm/routre-cli/binary/ (${hostTarget.pkg})`,
  );
}

// Pack everything.
for (const t of targets) {
  console.log(`packing ${t.pkg}...`);
  run("npm pack --pack-destination " + JSON.stringify(distDir), {
    cwd: join(npmDir, t.pkg),
    stdio: "inherit",
  });
}
console.log("packing routre-cli...");
run("npm pack --pack-destination " + JSON.stringify(distDir), {
  cwd: join(npmDir, "routre-cli"),
  stdio: "inherit",
});

console.log("\nDone. Tarballs in npm/dist/:");
for (const f of ["routre-cli", ...targets.map((t) => t.pkg)].sort()) {
  const p = join(distDir, `${f}-${version}.tgz`);
  console.log(`  ${existsSync(p) ? "✓" : "✗"} ${p}`);
}
