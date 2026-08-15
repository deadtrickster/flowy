/**
 * Puts web/dist/.gitkeep back after a build.
 *
 * vite empties outDir before it writes, so the one tracked file in there goes
 * with everything else, and `go build` on a fresh checkout needs it: console.go
 * embeds web/dist with `//go:embed all:web/dist`, and a pattern that matches
 * nothing is a build error rather than an empty directory.
 *
 * It is written with a line of text in it rather than as a zero-byte file
 * because a placeholder that says what it is for is worth more to whoever finds
 * it than an empty one. That is the whole of the reason. It was once written up
 * as a fix for the file being lost when this tree is copied out of the sandbox
 * it is built in - it is not one, and it never was: the copy was dropping the
 * file for reasons of its own, and the fix for that lives in the harness that
 * does the copying, not in this repo.
 *
 * The bytes here are the bytes committed at web/dist/.gitkeep. If they drift
 * apart, every build leaves the tree dirty, and the gate's last check says so.
 */

import { writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const keep = [
  "# keeps web/dist present so //go:embed all:web/dist builds before `npm run`",
  "# build has run. It has a line in it so that it says what it is for, which",
  "# is the only claim it makes: getting this tree copied somewhere intact is",
  "# not this file's job.",
  "# See .gitignore, which tracks this one file and ignores everything vite",
  "# writes beside it, and console.go, which embeds the directory.",
  "",
].join("\n");

const here = dirname(fileURLToPath(import.meta.url));
writeFileSync(resolve(here, "..", "dist", ".gitkeep"), keep);
