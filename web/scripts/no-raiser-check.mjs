/**
 * A row that predates the raiser field says NOTHING about a raiser, and a row
 * that has one names them.
 *
 * The presence case is raiser-check.mjs's, which drives a row raised out of a
 * message and asserts both names on the queue row and on the artifact page.
 * This is the absence case beside it, in its own file: the first cut of this
 * was written straight over that one, which destroyed a working check and made
 * its caller pass five arguments to a script expecting five different ones.
 *
 *   node scripts/no-raiser-check.mjs BASE_URL TOKEN PROJECT WITH_RAISER WITHOUT_RAISER
 *
 * The artifact page drew the clause whenever EITHER party was set, so every
 * pre-field row that had an assignee - most of the board - read "raised by
 * nobody on the record". Not false, and worse than uninformative: it reads as
 * an accusation of a missing record when the truth is that the row is older
 * than the field. The queue already had the rule and the reasoning written
 * down (web/src/routes/Todos.tsx); this page was the surface breaking it.
 * Reported by the operator, quoting the line.
 *
 * BOTH ARMS, because a page that never draws the clause passes an arm that only
 * checks the absence, and a page that always draws it passes one that only
 * checks the presence.
 */

import { chromium } from "playwright";

const [base, token, project, withRaiser, withoutRaiser] = process.argv.slice(2);
if (!base || !token || !project || !withRaiser || !withoutRaiser) {
  console.error(
    "usage: node scripts/no-raiser-check.mjs BASE_URL TOKEN PROJECT WITH_RAISER WITHOUT_RAISER",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const open = async (page, id) => {
  await page
    .goto(`${base}/p/${encodeURIComponent(project)}/memory/${encodeURIComponent(id)}`, {
      timeout: 20_000,
    })
    .catch(() => {});
  // THE WITNESS IS THE ASSIGNEE CLAUSE, NOT THE BREADCRUMB, and the difference
  // cost this check a false failure before it ever ran in the gate.
  //
  // The breadcrumb draws data-artifact-type from the PATH while the row is
  // still in flight - that is deliberate, so the page says where it is going
  // before it gets there - so waiting on it proves the app mounted and nothing
  // about whether the artifact arrived. Arm 1 then read "no raiser" off a page
  // that had no row yet, and arm 2 would have PASSED for the same reason, which
  // is the worse direction.
  //
  // The assignee clause is drawn whenever the row carries either party, and
  // both fixtures carry one, so its presence is the row being here.
  const loaded = page.locator("[data-artifact-assignee]");
  await loaded
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await loaded.count()) === 0) {
    die(`${id} never arrived on the page, so nothing below was measured`);
  }
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // 1. A ROW WITH A RAISER NAMES THEM.
  await open(page, withRaiser);
  const named = page.locator("[data-artifact-raiser]");
  if ((await named.count()) === 0) {
    die(`${withRaiser} was raised by somebody and the page says nothing about who`);
  }
  const who = await named.first().getAttribute("data-artifact-raiser");
  if (!who) die(`${withRaiser} draws a raiser clause with nobody in it: ${who}`);

  // 2. AND A ROW WITHOUT ONE SAYS NOTHING RATHER THAN "NOBODY ON THE RECORD".
  await open(page, withoutRaiser);
  if ((await page.locator("[data-artifact-raiser]").count()) > 0) {
    die(
      `${withoutRaiser} predates the raiser field and the page draws a raiser clause anyway - "raised by nobody on the record" is an accusation of a missing record, not a fact about the row`,
    );
  }
  // The assignee half being drawn is asserted by open() above, which waits for
  // it: an unowned row is work nobody has picked up, which is the thing to see,
  // and without that the arm above would pass for a page that dropped the whole
  // line rather than the clause.

  if (crashes.length > 0) die(`the console threw: ${crashes.join("; ")}`);
  console.log(
    `${withRaiser} names ${who}; ${withoutRaiser} says nothing and still says who carries it`,
  );
} finally {
  await browser.close();
}
