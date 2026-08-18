/**
 * A SILENT CONNECTION AND A DEAD ONE ARE THE SAME BYTES. Tell them apart.
 *
 *   node scripts/stream-liveness-check.mjs
 *
 * This is the arm that only exists because the transport changed. A poll that
 * stops produces failed requests somebody can see; a stream that stops produces
 * NOTHING, which is exactly what a stream with nothing to report produces. A
 * write only fails when somebody tries, so an open-and-silent connection reads
 * as healthy for as long as anybody cares to look.
 *
 * So the fixture is the node - scripts/stream-standin.mjs, told to greet and
 * then either heartbeat or go quiet without hanging up - and the assertion is
 * the DIFFERENCE between the two arms:
 *
 *   beating  -> the panel stays live, and its "as of" ADVANCES with no todo
 *               changing anywhere. A clock that read the last EVENT would
 *               freeze here while the connection was perfectly healthy.
 *   silent   -> the panel goes stale within a bounded time and SAYS SO, while
 *               keeping the rows on screen. An emptied list would be a false
 *               statement that the work is gone.
 *
 * One arm alone proves nothing: a page that always said "stale" would pass the
 * second, and a page that never did would pass the first.
 */

import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "..", "dist");
const PORT = Number(process.env.STREAM_PORT ?? 8903);
const base = `http://127.0.0.1:${PORT}`;

let failed = false;
const fail = (message) => {
  console.error(message);
  failed = true;
};

/** One run of the console against a stand-in in the given mode. */
async function run(mode, watchFor) {
  const standin = spawn(
    process.execPath,
    [resolve(here, "stream-standin.mjs"), dist, String(PORT), mode],
    { stdio: ["ignore", "pipe", "inherit"] },
  );
  await new Promise((ok, no) => {
    const giveUp = setTimeout(() => no(new Error("the stand-in did not start")), 10_000);
    standin.stdout.on("data", (chunk) => {
      if (String(chunk).includes("standin on")) {
        clearTimeout(giveUp);
        ok();
      }
    });
    standin.on("exit", (code) => no(new Error(`the stand-in exited with ${code}`)));
  });

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    await page.addInitScript(() => localStorage.setItem("flowy.token", "standin-token"));
    await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});
    const mark = page.locator("[data-stream-asof]").first();
    await mark.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
    const first = {
      asOf: await mark.getAttribute("data-stream-asof").catch(() => null),
      state: await mark.getAttribute("data-stream-state").catch(() => null),
    };
    await page.waitForTimeout(watchFor);
    const rows = await page.locator("li[data-todo-row]").count();
    return {
      first,
      last: {
        asOf: await mark.getAttribute("data-stream-asof").catch(() => null),
        state: await mark.getAttribute("data-stream-state").catch(() => null),
      },
      rows,
    };
  } finally {
    await browser.close();
    standin.kill();
  }
}

// A node that is connected and has nothing to say. The panel must stay live and
// its clock must move, because the heartbeat is what it reads.
const beating = await run("beating", 5000);
if (beating.last.state !== "live") {
  fail(
    `a heartbeating stream left the panel in state ${JSON.stringify(beating.last.state)}, want "live"`,
  );
} else if (beating.last.asOf === beating.first.asOf) {
  fail(`the "as of" did not advance against a heartbeating node (${beating.first.asOf}).

It is reading the last EVENT rather than the heartbeat. Nothing changed on this
stand-in and nothing was ever going to - a clock that needs a change to move
cannot tell a quiet node from a dead one.`);
} else {
  console.log(`a heartbeating stream keeps the panel live and its clock moving`);
}

// And the byte-identical case: greeted, open, and silent ever since. Long
// enough to pass the panel's own staleness window with room to spare.
const silent = await run("silent", 19_000);
if (silent.last.state !== "stale") {
  fail(`a stream that greeted and then went silent left the panel in state ${JSON.stringify(
    silent.last.state,
  )}, want "stale".

The socket was open the whole time and the node never wrote another byte. That
is indistinguishable from a quiet node unless the panel is watching the
heartbeat, and a board that cannot notice is a board that shows a frozen list as
though it were current.`);
} else if (silent.rows === 0) {
  fail(`the panel went stale and emptied the list.

The rows must stay: an empty queue under a heading that says what is outstanding
reads as "the work is done", which is a worse lie than a board a few seconds out
of date. Stale is a mark on the answer, not the removal of it.`);
} else {
  console.log(`an open, silent stream is called stale within the window, with the rows kept`);
}

process.exit(failed ? 1 : 0);
