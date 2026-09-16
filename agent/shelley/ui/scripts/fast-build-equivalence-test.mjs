#!/usr/bin/env node
// FAST_BUILD must change only how dist/ is compressed, never what it contains.
//
// CI's test steps build the UI with FAST_BUILD=1 (source maps dropped, gzip
// level 1 instead of 9) because it saves ~5s on each of the ~8 shelley jobs.
// That is only safe if the bytes the server ultimately serves are identical, so
// this test builds the UI both ways and compares the DECOMPRESSED payloads of
// every shipped asset. A regression here would mean CI is testing a different
// artifact than the one we release, which is exactly the class of bug that is
// invisible until production.
//
// Source maps are the one intended difference: fast builds drop them, so they
// are excluded from the comparison (and their absence is asserted instead).
// Dropping them also drops each bundle's trailing "//# sourceMappingURL=" line,
// so that comment is normalized away before comparing. Nothing else may differ:
// the assertion is on the executable content itself, not a size or file count.

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { gunzipSync } from "node:zlib";
import { readFileSync, readdirSync, rmSync, cpSync, statSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const uiDir = new URL("..", import.meta.url).pathname;

function build(env) {
  // Clean dist first. The build does not clear it, so without this the second
  // build inherits the first one's output: if fast mode ever stopped emitting an
  // asset, the leftover copy would make the comparison pass and hide exactly the
  // divergence this test exists to catch.
  rmSync(join(uiDir, "dist"), { recursive: true, force: true });
  execFileSync("node", ["scripts/build.js"], {
    cwd: uiDir,
    env: { ...process.env, ...env },
    stdio: "pipe",
  });
  const snapshot = mkdtempSync(join(tmpdir(), "ui-dist-"));
  cpSync(join(uiDir, "dist"), snapshot, { recursive: true });
  return snapshot;
}

// Payload identity: decompress .gz, hash the plaintext. Keyed by logical asset
// name so main.js and main.js.gz compare equal across the two builds.
function payloads(dir) {
  const out = new Map();
  for (const file of readdirSync(dir, { recursive: true })) {
    const name = String(file);
    const full = join(dir, name);
    if (!statSync(full).isFile()) continue;
    const stat = readFileSync(full);
    if (name.endsWith(".map") || name.endsWith(".map.gz")) continue;
    // build-info.json embeds a timestamp and commit state; it is expected to differ.
    if (name.endsWith("build-info.json")) continue;
    // checksums.json is derived from the compressed bytes, so it differs by design.
    if (name.endsWith("checksums.json")) continue;
    const logical = name.endsWith(".gz") ? name.slice(0, -3) : name;
    let bytes = name.endsWith(".gz") ? gunzipSync(stat) : stat;
    if (/\.(js|css)$/.test(logical)) {
      // Normalize the sourceMappingURL trailer (present only when maps are emitted).
      bytes = Buffer.from(
        bytes
          .toString("utf8")
          .replace(/\/\/# sourceMappingURL=[^\n]*\n?$/, "")
          .replace(/\/\*# sourceMappingURL=[^\n]*\*\/\n?$/, ""),
        "utf8",
      );
    }
    out.set(logical, createHash("sha256").update(bytes).digest("hex"));
  }
  return out;
}

let failures = 0;
function check(cond, msg) {
  if (cond) {
    console.log(`\u2713 ${msg}`);
  } else {
    console.error(`\u2717 ${msg}`);
    failures++;
  }
}

const normal = build({ FAST_BUILD: "", NO_SOURCEMAPS: "" });
const fast = build({ FAST_BUILD: "1" });

const a = payloads(normal);
const b = payloads(fast);

check(a.size > 0, `normal build produced assets (${a.size})`);
check(
  [...a.keys()].sort().join(",") === [...b.keys()].sort().join(","),
  "both builds ship the same set of assets",
);
for (const [name, hash] of a) {
  check(b.get(name) === hash, `identical decompressed payload: ${name}`);
}
check(
  readdirSync(fast, { recursive: true }).every((f) => !String(f).endsWith(".map.gz")),
  "fast build drops source maps",
);
check(
  readdirSync(normal, { recursive: true }).some((f) => String(f).endsWith(".map.gz")),
  "normal build keeps source maps",
);

rmSync(normal, { recursive: true, force: true });
rmSync(fast, { recursive: true, force: true });

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nFAST_BUILD is payload-equivalent to a normal build.");
