/**
 * THE TOKEN BAR IS ON /profile, AND NOWHERE ELSE.
 *
 *   node scripts/token-in-profile-check.mjs BASE_URL TOKEN
 *
 * It used to sit at the foot of the left rail on every page, with the bearer
 * token visible in an input - scaffolding parked in the product. The operator:
 * "yeah move it in profile. I dont use it and it wastes time."
 *
 * BOTH HALVES, because a move is a deletion plus an addition and either one
 * alone is a defect: gone from the rail, present on /profile, and STILL
 * WORKING there. A control that was moved and quietly broken is worse than one
 * left where it was - it is the same click and a different outcome.
 *
 * The pages walked are not just one: the rail is drawn by the shell on every
 * route, so a check that only looked at the overview would pass on a rail that
 * still carried it everywhere else.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/token-in-profile-check.mjs BASE_URL TOKEN");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // GONE FROM THE FURNITURE. Several routes, because the rail is the shell's
  // and is drawn on all of them.
  for (const path of ["/", "/todos", "/chat/general", "/projects"]) {
    await page.goto(`${base}${path}`, { timeout: 30_000 });
    await page.waitForTimeout(700);
    const inRail = await page.locator("[data-nav] [data-token-bar]").count();
    if (inRail > 0) {
      die(`${path} still draws the token bar in the left rail.

It is scaffolding on every page of the console, with a bearer token visible in
an input. It belongs on /profile.`);
    }
  }

  // PRESENT WHERE IT WAS MOVED TO.
  await page.goto(`${base}/profile`, { timeout: 30_000 });
  const bar = page.locator("[data-token-bar]");
  await bar.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await bar.count()) !== 1) {
    die(`/profile draws ${await bar.count()} token bars, and it should draw exactly one`);
  }

  // AND STILL WORKING. The input takes a value and the form is submittable -
  // asserted by ROLE, because a control that renders and cannot be used is the
  // failure mode a move introduces.
  const field = bar.locator("input").first();
  if ((await field.count()) === 0) {
    die("the token bar on /profile has no input, so a token cannot be pasted into it");
  }
  if (await field.isDisabled()) {
    die("the token bar's input on /profile is disabled");
  }
  await field.fill("a-token-that-will-not-resolve");
  if ((await field.inputValue()) !== "a-token-that-will-not-resolve") {
    die("the token bar's input on /profile does not take what is typed into it");
  }
  const submit = bar.locator('button[type="submit"], button:has-text("use")').first();
  if ((await submit.count()) === 0) {
    die("the token bar on /profile offers no way to submit what was pasted");
  }

  console.log(
    "the token bar is gone from the rail on four routes, and is on /profile with a working input and a submit",
  );
} finally {
  await browser.close();
}
