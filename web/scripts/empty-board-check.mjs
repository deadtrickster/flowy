/**
 * The overview renders on a node whose board is empty.
 *
 *   node scripts/empty-board-check.mjs BASE_URL TOKEN
 *
 * It did not. SpreadCard read `w.shares.length`, Go marshals an empty slice as
 * null, and WorkloadOf returns a nil Shares when nobody is carrying anything -
 * so /api/nag answered "shares": null, the render threw, React unmounted the
 * tree above it, and the WHOLE PAGE went blank with nothing said. Not the card:
 * the overview.
 *
 * It shipped and never showed, because the only board anybody looked at has
 * never been empty. That is the case this check exists for and it is why the
 * check needs a node OF ITS OWN with nothing on it - run against the suite's
 * seeded node it would pass on the day it was written and every day after,
 * measuring the seed rather than the code.
 *
 * ASSERTED ON THE PAGE HAVING CONTENT AT ALL, not on the card. The failure is
 * a blank document, so "the board card is missing" is the wrong question - a
 * broken card that renders nothing looks the same as a page that unmounted, and
 * only the second one takes the inbox and the room box with it.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/empty-board-check.mjs BASE_URL TOKEN");
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
  await page.waitForTimeout(2000);

  const text =
    (await page
      .locator("body")
      .innerText()
      .catch(() => "")) || "";
  if (crashes.length > 0) {
    die(`the overview threw on an empty board: ${crashes.join("; ")}`);
  }
  if (text.trim().length === 0) {
    die("the overview rendered nothing at all - a blank page is what an unmounted tree looks like");
  }
  // The parts that must survive, named individually: the failure took the whole
  // page, so asserting one of them would pass for a page that kept only it.
  for (const want of ["overview", "open a room", "inbox"]) {
    if (!text.toLowerCase().includes(want)) {
      die(`the overview is missing ${JSON.stringify(want)} on an empty board`);
    }
  }
  // And the board card says the empty case in words rather than drawing an
  // empty table or vanishing.
  if (!text.includes("nobody is carrying anything")) {
    die("the board card does not say the board is empty; it should say so, not disappear");
  }
  console.log("the overview renders on an empty board and says so");
} finally {
  await browser.close();
}
