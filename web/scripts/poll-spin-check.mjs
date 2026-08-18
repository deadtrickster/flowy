/**
 * The room poll must not flood the node it is reading from.
 *
 * This is the regression check for a real failure: the watch loop advanced its
 * cursor only when events arrived, so against a node whose cursor did not move
 * it re-asked instantly forever - 2917 requests in 20 seconds measured on the
 * live node, 5671 in 10 seconds measured here. Every other check passed
 * throughout, because a flood renders exactly like a healthy poll.
 *
 * It runs a real browser against the shipped bundle and the stand-in node next
 * door, and asserts two things that have to be asserted together:
 *
 *   the room RENDERED             - a page that never mounted makes no requests
 *                                   either, and would otherwise score a perfect
 *                                   zero. The first time this was measured by
 *                                   hand it did exactly that: 0 requests,
 *                                   because a stubbed payload had crashed the
 *                                   app before mount. A small number is the
 *                                   good result here, so a run that never
 *                                   happened passes unless it is made to prove
 *                                   it happened.
 *   the request count is bounded  - and the bound is nowhere near either side,
 *                                   so this discriminates rather than
 *                                   describing whatever the loop does today.
 *
 * The ceiling is generous on purpose. A correct loop makes about one request a
 * second here (its floor when a wait neither blocks nor advances), the broken
 * one made several hundred, and anything between 25 and 500 is a loop that has
 * lost its pacing whatever the cause. Precision would only make this flaky on a
 * slow VM; the failure it catches is three orders of magnitude wide.
 */

import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "..", "dist");

// 0 asks the OS for a free port and the stand-in prints the one it got. This
// used to be a hardcoded 8899 shared with fresh-check's 8901, and two suites on
// one box raced them: the loser's EADDRINUSE was reported as a failure of the
// feature under test. SPIN_PORT still overrides, as an escape hatch rather than
// the default.
const PORT = Number(process.env.SPIN_PORT ?? 0);
const SECONDS = Number(process.env.SPIN_SECONDS ?? 6);
const CEILING = Number(process.env.SPIN_CEILING ?? 25);
let base = `http://127.0.0.1:${PORT}`;

const fail = (message) => {
  console.error(message);
  process.exitCode = 1;
};

const standin = spawn(process.execPath, [resolve(here, "standin-node.mjs"), dist, String(PORT)], {
  stdio: ["ignore", "pipe", "inherit"],
});

// Wait for the line it prints when it is listening rather than sleeping a
// guessed interval, so a slow VM does not turn into a flaky check.
await new Promise((ok, no) => {
  const giveUp = setTimeout(() => no(new Error("the stand-in node did not start")), 10_000);
  standin.stdout.on("data", (chunk) => {
    const said = String(chunk).match(/standin on (\d+)/);
    if (said) {
      // The port the OS actually handed out - the only one that exists when
      // PORT is 0.
      base = `http://127.0.0.1:${said[1]}`;
      clearTimeout(giveUp);
      ok();
    }
  });
  standin.on("exit", (code) => no(new Error(`the stand-in node exited with ${code}`)));
});

let browser;
try {
  browser = await chromium.launch();
  const page = await browser.newPage();
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript(() => localStorage.setItem("flowy.token", "standin-token"));
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});
  await page.waitForTimeout(2000);

  // Zeroed after load, so the first room read and the mount traffic are not
  // charged to the loop this is measuring.
  const before = await (await fetch(`${base}/__waits`)).json();
  await page.waitForTimeout(SECONDS * 1000);
  const after = await (await fetch(`${base}/__waits`)).json();
  const waits = after.waits - before.waits;

  const rendered = await page
    .locator("h1", { hasText: "#general" })
    .first()
    .isVisible()
    .catch(() => false);

  if (!rendered) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    fail(`the room never rendered, so ${waits} requests measures nothing${errors}`);
  } else if (waits > CEILING) {
    fail(`the room poll made ${waits} requests in ${SECONDS}s against a node whose cursor never moves
the ceiling is ${CEILING}. A loop that neither blocks nor advances its cursor must pause,
or one open tab is a denial of service aimed at your own node.`);
  } else {
    console.log(
      `${waits} requests in ${SECONDS}s (ceiling ${CEILING}), and the room rendered while making them`,
    );
  }
} finally {
  await browser?.close();
  standin.kill();
}
