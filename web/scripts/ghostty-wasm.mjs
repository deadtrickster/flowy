/**
 * Put ghostty's wasm where ghostty looks for it.
 *
 * WHY THIS SCRIPT EXISTS AT ALL. ghostty-web's init() takes no URL: it probes
 * its own module URL, then ./ghostty-vt.wasm, then /ghostty-vt.wasm. Vite does
 * not emit an asset nothing imports, so a build with no step like this one is
 * GREEN and produces a console whose terminal panel throws the moment somebody
 * presses Run - a dead button arriving through the build rather than the code.
 *
 * COPIED RATHER THAN COMMITTED. Two megabytes of binary in the repo would be a
 * second copy of a dependency, free to drift from the version in
 * package-lock.json, and nothing would notice until the drift mattered. Copying
 * from node_modules means the served file is by construction the one the
 * installed library expects.
 *
 * It runs BEFORE the build, into public/, because that is the directory vite
 * copies verbatim to the root of dist - which is where /ghostty-vt.wasm has to
 * be answerable from.
 */
import { copyFileSync, existsSync, mkdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const web = dirname(here);
const from = join(web, "node_modules", "ghostty-web", "ghostty-vt.wasm");
const to = join(web, "public", "ghostty-vt.wasm");

if (!existsSync(from)) {
  // REFUSED, NOT SKIPPED. Carrying on without the wasm builds a console that
  // looks complete and cannot open a terminal, which is worse than not
  // building at all - the failure would be found by a person pressing a button
  // rather than by this line.
  console.error(
    `ghostty-web's wasm is not at ${from} - run npm ci. Building without it produces a` +
      " console whose terminal panel throws on Run.",
  );
  process.exit(1);
}
mkdirSync(dirname(to), { recursive: true });
copyFileSync(from, to);
console.log(`ghostty-vt.wasm -> public/ (${statSync(to).size} bytes)`);
