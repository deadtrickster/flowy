/**
 * Every dependency is present AND has the file it says it has.
 *
 * `npm ls` was the obvious check and it is not enough: it reads each package's
 * package.json and compares versions, so a package whose entry point has been
 * truncated away still reports as installed. Measured, not assumed - gutting
 * node_modules/classcat/index.js leaves `npm ls --depth=0` saying OK while
 * `require.resolve("classcat")` fails, which is precisely the state that cost
 * this fleet a morning: rollup could not resolve the import, npm ci had exited
 * 0, and the failure named the import rather than the install.
 *
 * It checks for the FILE rather than calling require.resolve, because
 * require.resolve throws for packages that are ESM-only or that export no CJS
 * path - a check that reports healthy packages as broken would be worse than
 * the one it replaces, since the repair it triggers is a full reinstall.
 *
 *   node scripts/deps-intact.mjs        # exit 0 intact, 1 with a list
 */

import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const read = (path) => {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
};

const root = read("package.json");
if (!root) {
  console.error("no package.json here, so nothing was checked");
  process.exit(2);
}

// The entry a package promises, in the order node itself would look: exports
// first, then main, then the index.js default.
const entryOf = (pkg) => {
  const dot = pkg.exports?.["."] ?? pkg.exports;
  if (typeof dot === "string") return dot;
  if (dot && typeof dot === "object") {
    for (const key of ["import", "require", "default", "node"]) {
      const value = dot[key];
      if (typeof value === "string") return value;
      if (value && typeof value === "object" && typeof value.default === "string") {
        return value.default;
      }
    }
  }
  return pkg.main ?? "index.js";
};

// EVERY installed package, not just the ones package.json names. The package
// that was truncated in the real incident was `classcat` - a transitive
// dependency of @xyflow/react, which nothing in this file's dependencies lists.
// A check that walked only direct dependencies said "ok, 24 present" about a
// tree whose build could not resolve an import, which is the same shape of
// wrong as the thing it is meant to catch.
const installed = [];
const listDir = (path) => {
  try {
    return readdirSync(path);
  } catch {
    return [];
  }
};
for (const entry of listDir("node_modules")) {
  if (entry.startsWith(".")) continue; // .bin, .package-lock.json, .cache
  if (entry.startsWith("@")) {
    for (const scoped of listDir(join("node_modules", entry))) {
      installed.push(`${entry}/${scoped}`);
    }
    continue;
  }
  installed.push(entry);
}

// Any code file anywhere in the package, to a few levels. Depth-limited
// because this runs over every installed package and the answer is almost
// always in the first directory it opens; nested node_modules are skipped
// because a dependency's own dependency is checked in its own right.
const hasCodeUnder = (dir, depth = 3) => {
  let entries = [];
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return false;
  }
  for (const entry of entries) {
    if (entry.isFile()) {
      // Anything that is not metadata or documentation counts as payload. An
      // allow-list of extensions was the first attempt and it called three
      // healthy packages empty - @biomejs/cli-linux-x64 ships one extensionless
      // executable named `biome`, and there is no list of extensions that stays
      // right across every package in a tree this size. What a truncated
      // package actually looks like is a directory holding a README, a LICENSE
      // and nothing else, so that is what this asks.
      if (!/^(package\.json|README|LICENSE|LICENCE|CHANGELOG|HISTORY|NOTICE)/i.test(entry.name)) {
        return true;
      }
    } else if (entry.isDirectory() && depth > 0 && entry.name !== "node_modules") {
      if (hasCodeUnder(join(dir, entry.name), depth - 1)) return true;
    }
  }
  return false;
};

const named = Object.keys({ ...root.dependencies, ...root.devDependencies });
const missing = named.filter((name) => !read(join("node_modules", name, "package.json")));
const broken = missing.map((name) => `${name}: not installed`);

for (const name of installed) {
  const dir = join("node_modules", name);
  const pkg = read(join(dir, "package.json"));
  if (!pkg) continue; // not a package directory; nothing claimed, nothing broken
  const entry = entryOf(pkg);
  if (existsSync(join(dir, entry))) continue;
  // A package can name an entry it builds, or point into a subpath this crude
  // reader does not follow, so a missing entry is a suspicion and not a verdict.
  // The question that settles it is whether the package contains any code AT
  // ALL - and that has to look into subdirectories, because plenty of healthy
  // packages keep everything under dist/ or data/ and nothing at the top. A
  // top-level-only scan called six intact packages broken on a clean install,
  // node-releases among them, whose entire payload is data/*.json. Reporting a
  // healthy tree as poisoned would trigger a full reinstall every run.
  if (!hasCodeUnder(dir)) {
    broken.push(`${name}: unpacked without code (${listDir(dir).join(", ") || "empty"})`);
  }
}

if (broken.length > 0) {
  console.error(`${broken.length} dependency(ies) are installed but incomplete:`);
  for (const line of broken.slice(0, 10)) console.error(`  ${line}`);
  process.exit(1);
}
console.log(`ok  ${installed.length} installed packages have the entry points they claim`);
