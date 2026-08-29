/**
 * THE PASTE-A-TOKEN BOX IS ON /profile - AND THE WAY OUT STAYS IN THE RAIL.
 *
 *   node scripts/token-in-profile-check.mjs BASE_URL TOKEN
 *
 * It sat at the foot of the left rail on every page with the bearer token
 * visible in an input. The operator: "yeah move it in profile. I dont use it
 * and it wastes time."
 *
 * THE FIRST VERSION OF THIS CHECK ASSERTED THE WRONG THING. It required
 * [data-token-bar] to be absent from the rail - and the bar also holds the
 * LOG-IN LINK and the LOG-OUT BUTTON. The move passed this check and took the
 * way in and the way out off every page of the console; three other checks
 * failed and said so. TokenBar's own note had already written the rule:
 * "being unable to LEAVE is the same defect one step later".
 *
 * So what moved is the INPUT, and what stays is the furniture. Both are
 * asserted, in both places, because a move is a deletion plus an addition and
 * either alone is a defect.
 *
 * Several routes, not one: the rail is the shell's and is drawn on all of them.
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
    // THE INPUT IS GONE FROM THE FURNITURE.
    const pasteInRail = await page.locator("[data-nav] [data-token-paste]").count();
    if (pasteInRail > 0) {
      die(`${path} still draws the paste-a-token box in the left rail.

It is a credential field on every page of the console. It belongs on /profile.`);
    }

    // AND THE WAY OUT IS STILL THERE. This is the arm the first version of this
    // check did not have, and its absence let a change ship that removed the
    // log-out button from the whole console.
    const out = await page.locator("[data-nav] [data-log-out], [data-nav] [data-log-in]").count();
    if (out === 0) {
      die(`${path} offers neither a way in nor a way out in the rail.

Whichever the reader needs is the one that must be there - log in when nobody is
signed in, log out when somebody is - and it is furniture, so it is on every
page.`);
    }
  }

  // PRESENT WHERE IT WAS MOVED TO.
  await page.goto(`${base}/profile`, { timeout: 30_000 });
  const bar = page.locator("[data-token-paste]");
  await bar.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await bar.count()) !== 1) {
    die(`/profile draws ${await bar.count()} paste-a-token boxes, and it should draw exactly one`);
  }

  // AND STILL WORKING. The input takes a value and the form is submittable -
  // asserted by ROLE, because a control that renders and cannot be used is the
  // failure mode a move introduces.
  const field = bar.locator("input").first();
  if ((await field.count()) === 0) {
    die("the paste-a-token box on /profile has no input, so a token cannot be pasted into it");
  }
  if (await field.isDisabled()) {
    die("the paste-a-token box on /profile is disabled");
  }
  await field.fill("a-token-that-will-not-resolve");
  if ((await field.inputValue()) !== "a-token-that-will-not-resolve") {
    die("the paste-a-token box on /profile does not take what is typed into it");
  }
  const submit = bar.locator('button[type="submit"], button:has-text("use")').first();
  if ((await submit.count()) === 0) {
    die("the paste-a-token box on /profile offers no way to submit what was pasted");
  }

  console.log(
    "the paste box is gone from the rail on four routes and the way out is still there, and it is on /profile with a working input and a submit",
  );
} finally {
  await browser.close();
}
