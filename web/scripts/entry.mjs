/**
 * Which built file the console actually loads.
 *
 * ONE ANSWER, SHARED, because two scripts guessed it independently and both
 * guessed the same way: readdirSync(assets).find(endsWith(".js")), the first
 * name in DIRECTORY ORDER. That was right only while the build emitted a single
 * chunk. The first thing to code-split added __vite-browser-external-*.js, which
 * sorts before index-*.js, and both checks silently began describing a chunk
 * nobody loads - render-check reported "the app mounted nothing into #root" on a
 * console that mounts, and fresh-check made a current tab reload forever because
 * the stand-in node claimed a bundle the page was not running.
 *
 * index.html names its entry. So it is asked, not guessed - the same thing
 * console.go's bundleOf does, for the same reason.
 */
import { existsSync, readFileSync } from "node:fs";
import { basename, join } from "node:path";

export function entryBundle(dist) {
  const index = readFileSync(join(dist, "index.html"), "utf8");
  const found = /<script[^>]+type="module"[^>]+src="([^"]+)"/.exec(index);
  if (!found) {
    throw new Error(
      "web/dist/index.html loads no module script - there is no console to mount, and a" +
        " check that carried on here would be describing an empty page",
    );
  }
  const name = basename(found[1]);
  if (!existsSync(join(dist, "assets", name))) {
    throw new Error(`index.html loads ${found[1]} and web/dist/assets has no ${name}`);
  }
  return name;
}
