/**
 * A tab left open across a deploy has to notice, reload itself once, and stop.
 *
 * Both halves are load-bearing and only one of them is obvious. Reloading is
 * the feature; RELOADING EXACTLY ONCE is what keeps the feature from being
 * worse than the bug. If the reload does not change what is running - a cached
 * index, a proxy serving yesterday's html - the page is still stale afterwards,
 * and a page that reloads whenever it is stale reloads forever. That is not a
 * degraded console, it is an unusable one.
 *
 * So this runs the shipped bundle in a real browser against a stand-in node
 * told to claim a console the page is not running, and counts loads at the
 * SERVER. Asking the page how many times it has reloaded is asking a process
 * that keeps restarting to remember something.
 *
 *   stale node   -> exactly one extra load, then the banner, then nothing
 *   current node -> no reload at all
 *
 * The second case is the one that catches an over-eager version of this: a
 * check that only tested the stale case would pass a page that reloads all the
 * time.
 */

import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

import { entryBundle } from "./entry.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "..", "dist");
// ASK FOR NOTHING AND BE TOLD. 0 lets the OS pick, and the stand-in prints the
// port it actually got - so two of these running at once on one box cannot
// collide. They used to share a hardcoded number, and the loser's EADDRINUSE
// was reported as a failure of the feature under test rather than of the
// harness: a red naming something that was fine, on somebody else's branch.
//
// FRESH_PORT still overrides, for anybody who needs to point a browser at it by
// hand. It is an escape hatch, not the default.
const PORT = Number(process.env.FRESH_PORT ?? 0);
let base = `http://127.0.0.1:${PORT}`;

// What the shipped index actually loads, so "the same console" is the truth
// rather than a string typed twice.
const running = entryBundle(dist);
if (!running) {
  console.error("web/dist/assets holds no javascript bundle");
  process.exit(1);
}

let failed = false;
const fail = (message) => {
  console.error(message);
  failed = true;
};

/** One run of the app against a stand-in claiming to serve `claims`. */
async function run(claims, seconds) {
  const standin = spawn(
    process.execPath,
    [resolve(here, "standin-node.mjs"), dist, String(PORT), claims],
    { stdio: ["ignore", "pipe", "inherit"] },
  );
  await new Promise((ok, no) => {
    const giveUp = setTimeout(() => no(new Error("the stand-in node did not start")), 10_000);
    standin.stdout.on("data", (chunk) => {
      const said = String(chunk).match(/standin on (\d+)/);
      if (said) {
        // The port the OS handed out, which is the only one that exists when
        // PORT is 0 - everything below talks to this rather than to the
        // number we asked for.
        base = `http://127.0.0.1:${said[1]}`;
        clearTimeout(giveUp);
        ok();
      }
    });
    standin.on("exit", (code) => no(new Error(`the stand-in node exited with ${code}`)));
  });

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    await page.addInitScript(() => localStorage.setItem("flowy.token", "standin-token"));
    await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});
    await page.waitForTimeout(seconds * 1000);
    const { loads } = await (await fetch(`${base}/__loads`)).json();
    const banner = await page
      .getByText("running an older console", { exact: false })
      .first()
      .isVisible()
      .catch(() => false);
    return { loads, banner };
  } finally {
    await browser.close();
    standin.kill();
  }
}

// A node serving a console this page is not running. One reload, then the
// banner: the reload cannot help, because the stand-in keeps serving the same
// bundle whatever it claims, which is exactly the cached-index case.
const stale = await run("index-A-CONSOLE-THIS-PAGE-IS-NOT-RUNNING.js", 6);
if (stale.loads < 2) {
  fail(`a tab on an older console did not reload: ${stale.loads} load(s), expected 2`);
} else if (stale.loads > 2) {
  fail(
    `a tab on an older console reloaded ${stale.loads - 1} times, expected once.
A reload that does not fix the mismatch must not be tried again - that is a reload loop,
and a console that reloads forever is worse than one that is merely out of date.`,
  );
} else if (!stale.banner) {
  fail("after the reload failed to help, nothing on the page said the tab is behind");
} else {
  console.log("a stale tab reloaded once, then said so and stopped");
}

// And the ordinary case, which is every page load that is not a deploy.
const current = await run(running, 4);
if (current.loads !== 1) {
  fail(`a tab on the current console reloaded: ${current.loads} loads, expected 1`);
} else if (current.banner) {
  fail("a tab on the current console claims to be behind");
} else {
  console.log("a current tab did not reload and said nothing");
}

process.exit(failed ? 1 : 0);
