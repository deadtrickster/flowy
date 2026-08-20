/**
 * The console shows a token its own readers, and says they are its own.
 *
 *   node scripts/readers-panel-check.mjs BASE_URL TOKEN QUIET_READER LIVE_READER
 *
 * WHY. Measured 2026-08-20: three seats compared /api/inbox/readers in the room
 * and found abandoned readers in all three lists - declared by a console load on
 * the 18th and having acknowledged nothing in two days. Each of us then blamed
 * another seat's rows, twice, because one label is worn by one row per principal
 * and none of us checked whose we were reading.
 *
 * `api.inboxReaders()` had been in the client since it was written and no route
 * drew it. The reason nobody had noticed their own abandoned reader is that
 * there was nowhere to look.
 *
 * THREE THINGS ASSERTED, and the third is the one that matters:
 *
 *   the rows are there at all, by label
 *   a reader that has not moved its mark in hours offers a way to forget it,
 *     and one that just moved does NOT - deleting a live reader's row sends its
 *     waiter back to the head and silently skips everything in between
 *   the panel says whose these are, in words, on the page
 *
 * The last is not decoration. It is the entire fix for a mistake three
 * independent readers of this data made in a row within ten minutes.
 */

import { chromium } from "playwright";

const [base, token, quietReader, liveReader] = process.argv.slice(2);
if (!base || !token || !quietReader || !liveReader) {
  console.error(
    "usage: node scripts/readers-panel-check.mjs BASE_URL TOKEN QUIET_READER LIVE_READER",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/profile`, { timeout: 20_000 }).catch(() => {});

  const panel = page.locator("[data-your-readers]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (crashes.length > 0) die(`the profile page threw: ${crashes.join("; ")}`);
  if ((await panel.count()) === 0) die("no readers panel on /profile at all");

  for (const want of [quietReader, liveReader]) {
    if ((await page.locator(`[data-reader="${want}"]`).count()) === 0) {
      die(`the panel does not list ${want}, which the node says this token holds`);
    }
  }

  // The guard, both directions. A "forget" on every row would be the defect
  // this check exists to prevent, and a "forget" on none of them would pass a
  // panel that offers nothing.
  if ((await page.locator(`[data-reader-forget="${quietReader}"]`).count()) === 0) {
    die(`${quietReader} has not moved its mark in days and the panel offers no way to forget it`);
  }
  if ((await page.locator(`[data-reader-forget="${liveReader}"]`).count()) !== 0) {
    die(`${liveReader} moved its mark moments ago and the panel offers to delete it`);
  }

  // And it says whose. Asserted on the words, because the words ARE the fix.
  const text = (await panel.innerText().catch(() => "")) || "";
  if (!text.includes("only ever yours")) {
    die(`the panel does not say whose readers these are:\n${text}`);
  }
  // It must not call a quiet reader stuck: idle and stuck are indistinguishable
  // from these columns, and a confident wrong answer is the defect this fleet
  // found six times in one night.
  if (/\bstuck\b|\bdead\b/i.test(text)) {
    die(`the panel calls a reader stuck, which this data cannot know:\n${text}`);
  }

  if (crashes.length > 0) die(`the panel threw: ${crashes.join("; ")}`);
  console.log(`the panel lists ${quietReader} with a way to forget it, and ${liveReader} without`);
} finally {
  await browser.close();
}
