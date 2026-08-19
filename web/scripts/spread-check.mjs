/**
 * A person can see how the work is spread, and which line it crossed.
 *
 *   node scripts/spread-check.mjs BASE_URL TOKEN TOP_SEAT
 *
 * The probe has been on the node since it moved off board-nag.sh - shares per
 * seat, both thresholds, a verdict - and no surface a person sits in front of
 * drew any of it, so four seats each recomputed it from /api/artifacts instead.
 * Twenty times in one session, measured on the commands issued. A door nobody
 * uses is the same as no door.
 *
 * TOP_SEAT is who the caller's board says carries the most, so the check asserts
 * the console names the same party the node does rather than merely drawing
 * something. A panel that rendered a plausible bar for the wrong seat would pass
 * any check that only asked whether a bar was there.
 */

import { chromium } from "playwright";

const [base, token, top] = process.argv.slice(2);
if (!base || !token || !top) {
  console.error("usage: node scripts/spread-check.mjs BASE_URL TOKEN TOP_SEAT");
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
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});

  // The witness that the panel painted, which is also arm 1: the counts are
  // there at all. Without it every claim below is answered the same way by a
  // page that never loaded.
  const counts = page.locator("[data-nag-counts]");
  await counts
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await counts.count()) === 0) {
    die("the overview draws no board counts - the probe is on the node and nowhere a person looks");
  }

  // 2. THE SEAT THE NODE NAMES IS THE SEAT THE PAGE NAMES.
  const share = page.locator(`[data-spread-share="${top}"]`);
  if ((await share.count()) === 0) {
    const drawn = await page
      .locator("[data-spread-share]")
      .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-spread-share")));
    die(`the node says ${top} carries the most and the page draws ${JSON.stringify(drawn)}`);
  }
  const open = await share.first().getAttribute("data-spread-open");
  if (!open || Number(open) < 1) {
    die(`${top} is drawn carrying ${open} rows`);
  }

  // 3. AND THE SENTENCE SAYS WHICH LINE, not just the verdict word.
  //
  // `check` and `rebalance` mean different things to do, so a panel that
  // printed the word and not the number it crossed leaves the reader doing the
  // arithmetic this whole probe exists to end. On an even board it says neither,
  // which is the third legitimate answer and not a failure.
  const said = ((await page.locator("body").textContent()) || "").toLowerCase();
  if (said.includes("over the") && !/over the \d+% line/.test(said)) {
    die(`the page says a line was crossed without naming it: ${said.slice(0, 200)}`);
  }
  if (said.includes("hand some back") && !said.includes("80%")) {
    die("the page tells somebody to hand work back without saying which line that is");
  }

  if (crashes.length > 0) die(`the console threw drawing the spread: ${crashes.join("; ")}`);
  console.log(`the overview draws the board and names ${top} carrying ${open}`);
} finally {
  await browser.close();
}
