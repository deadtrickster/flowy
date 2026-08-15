/**
 * Puts web/dist/.gitkeep back after a build.
 *
 * vite empties outDir before it writes, so the one tracked file in there goes
 * with everything else, and `go build` on a fresh checkout needs it: console.go
 * embeds web/dist with `//go:embed all:web/dist`, and a pattern that matches
 * nothing is a build error rather than an empty directory.
 *
 * It is written with a line of text in it rather than as a zero-byte file. A
 * placeholder only has to exist, so being empty looks like the natural way to
 * write one - but an empty file is exactly what tooling that moves a tree
 * around drops: archivers and copy paths that skip zero-length entries leave
 * the directory behind, and the next `go build` in that copy fails on the
 * embed with nothing in the tree to say why. One line of content survives all
 * of them, and says what the file is for.
 *
 * The bytes here are the bytes committed at web/dist/.gitkeep. If they drift
 * apart, every build leaves the tree dirty, and the gate's last check says so.
 */

import { writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const keep = [
  "# keeps web/dist present so //go:embed all:web/dist builds before `npm run`",
  "# build has run. Not empty on purpose: a zero-byte file does not survive",
  "# every way this tree is copied around, and losing it breaks `go build`.",
  "# See .gitignore, which tracks this one file and ignores everything vite",
  "# writes beside it, and console.go, which embeds the directory.",
  "",
].join("\n");

const here = dirname(fileURLToPath(import.meta.url));
writeFileSync(resolve(here, "..", "dist", ".gitkeep"), keep);
