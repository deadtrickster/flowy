/**
 * A link says what it points at, and the page it lands on agrees.
 *
 *   node scripts/reference-check.mjs BASE_URL TOKEN PROJECT ARTIFACT TYPE
 *
 * A reference is (project, type, id). It was passed as a bare id and assembled
 * into a route by every caller that wanted one - thirteen of them, each
 * restating the same convention by hand, and one of them restating it wrong.
 * That cost a debugging session and three withdrawn theories about a 404 that
 * was never about the link shape at all: see 01M08FK999.
 *
 * The type segment is the part nothing checked. ArtifactView fetches by ID and
 * never read the segment, so a wrong one routed correctly and showed up only as
 * a breadcrumb contradicting the badge two lines below it. Two statements about
 * one row on one screen, and the wrong one is the one a reader meets first.
 *
 * So this asks the two questions that a builder alone cannot answer:
 *
 *   1. The page states the ROW's type, whatever the link claimed. Driven with a
 *      deliberately wrong segment, because a page that echoes the path passes
 *      any test that uses the right one.
 *   2. The link a list draws lands on the row it names. Followed, not parsed:
 *      a path that is well-formed and points at nothing is exactly the failure
 *      the hand-built ones produced.
 *
 * Both arms need the same row, so the caller passes one it has just written and
 * knows the type of. TYPE is what the node says it is, not what the URL will
 * say - the check exists to tell those apart.
 */

import { chromium } from "playwright";

const [base, token, project, id, type] = process.argv.slice(2);
if (!base || !token || !project || !id || !type) {
  console.error("usage: node scripts/reference-check.mjs BASE_URL TOKEN PROJECT ARTIFACT TYPE");
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

  // 1. A LINK THAT CLAIMS THE WRONG TYPE DOES NOT MAKE THE PAGE SAY IT.
  //
  // `artifact` is the segment the worklog used to hardcode and is not a type
  // any row has, so it is the exact claim that was being made and never
  // checked.
  await page
    .goto(`${base}/p/${encodeURIComponent(project)}/artifact/${encodeURIComponent(id)}`, {
      timeout: 20_000,
    })
    .catch(() => {});
  const crumb = page.locator("[data-artifact-type]");
  await crumb
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await crumb.count()) === 0) {
    die(
      `${id} did not paint - the page says nothing about its type, so nothing below was measured`,
    );
  }
  const said = await crumb.first().getAttribute("data-artifact-type");
  if (said === "artifact") {
    die(
      `the page repeats the path's "artifact", which is not a type any row has - the breadcrumb is echoing the link instead of reading the row`,
    );
  }
  if (said !== type) {
    die(`the page calls ${id} a ${said}; the node calls it a ${type}`);
  }

  // 2. AND A LINK THE CONSOLE DREW LANDS ON THE ROW IT NAMES.
  //
  // Followed rather than read: a path can be well-formed, carry the right three
  // segments, and point at nothing. The board is where the most links to rows
  // are drawn, so it is where a broken builder shows up first.
  await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});
  const link = page.locator(`a[href*="/${encodeURIComponent(id)}"]`);
  await link
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await link.count()) === 0) {
    die(`the board draws no link to ${id}, so this arm measured nothing`);
  }
  const href = await link.first().getAttribute("href");
  const parts = (href || "").split("/").filter(Boolean);
  if (parts.length !== 4 || parts[0] !== "p") {
    die(`the board links ${id} as ${href}, which is not /p/project/type/id`);
  }
  if (decodeURIComponent(parts[2]) !== type) {
    die(
      `the board links ${id} as a ${decodeURIComponent(parts[2])} and the node calls it a ${type} - the segment is a remembered convention rather than the row's own type`,
    );
  }
  await page.goto(`${base}${href}`, { timeout: 20_000 }).catch(() => {});
  const landed = page.locator("[data-artifact-type]");
  await landed
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await landed.count()) === 0) {
    die(`following the board's own link to ${id} painted no artifact`);
  }

  if (crashes.length > 0) {
    die(`the console threw while drawing references: ${crashes.join("; ")}`);
  }
  console.log(`${id} is a ${type} by every link and by the page a wrong link lands on`);
} finally {
  await browser.close();
}
