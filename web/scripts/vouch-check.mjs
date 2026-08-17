/**
 * A vouched worklog entry, on the page, in a real browser, asserted on ELEMENTS.
 *
 *   node scripts/vouch-check.mjs BASE_URL TOKEN VOUCHED_WHAT SUBJECT WRITER AUTHORED_WHAT
 *
 * THIS IS THE ONE THAT MATTERS. The drainer writes worklog entries on behalf of
 * runs - the harness knows the run id and the verify status and cannot lie about
 * whether the gate passed, so it is the right author - but an entry written BY
 * the harness ABOUT an agent must never appear as that agent's own entry. That is
 * the impersonation shape this project has open, and a marker on the row that no
 * reader is shown has bought nothing.
 *
 * Four claims, and the last is the one that discriminates:
 *
 *   - the vouched row is marked vouched, on its own attribute
 *   - it names the SUBJECT as whose work it is
 *   - it still names the WRITER as who wrote it, so the two are distinguishable
 *     on the row rather than merged into one byline
 *   - an ordinary entry on the same page is NOT marked
 *
 * Without the last, a view that marked every entry vouched passes the first
 * three, and a marker that is always on is a marker nobody reads.
 */

import { chromium } from "playwright";

const [base, token, vouched, subject, writer, authored] = process.argv.slice(2);

if (!base || !token || !vouched || !subject || !writer || !authored) {
  console.error(
    "usage: node scripts/vouch-check.mjs BASE_URL TOKEN VOUCHED_WHAT SUBJECT WRITER AUTHORED_WHAT",
  );
  process.exit(2);
}

/** die prints why, with what the list actually held, and stops. */
const die = async (why, list) => {
  let shown = "";
  try {
    shown = list ? await list.innerText() : "";
  } catch {
    shown = "(the list could not be read)";
  }
  console.error(`${why}\nthe list holds:\n${shown}`);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/worklog`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="worklog entries"]');
  const rows = list.locator("li[data-worklog-entry]");

  try {
    await rows.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(`/worklog rendered no entries at all${errors}`);
    process.exit(1);
  }

  const rowFor = (what) =>
    rows.filter({ has: page.locator(`[data-worklog-what]:has-text("${what}")`) });

  const vouchedRow = rowFor(vouched);
  if ((await vouchedRow.count()) === 0) {
    await die(`the vouched entry ${JSON.stringify(vouched)} is not on the page`, list);
  }

  // Marked, on the row's own attribute rather than found somewhere in the page's
  // text - a string that appears in two places is not evidence about either.
  if ((await vouchedRow.first().getAttribute("data-vouched")) === null) {
    await die(
      `the entry ${JSON.stringify(vouched)} was written by ${writer} about ${subject}'s work and the page does not mark it vouched, so it reads as ${subject}'s own entry`,
      list,
    );
  }

  // Whose work it is. The cell draws a short id and keeps the whole one on its
  // title, so the title is what this asserts on: the id is the attribution.
  const named = await vouchedRow.first().getAttribute("data-worklog-subject");
  if (named !== subject) {
    await die(
      `the vouched entry says its subject is ${JSON.stringify(named)}, want ${JSON.stringify(subject)}`,
      list,
    );
  }
  const shownSubject = await vouchedRow
    .first()
    .locator("[data-worklog-subject-id]")
    .getAttribute("title");
  if (shownSubject !== subject) {
    await die(
      `the row shows the subject as ${JSON.stringify(shownSubject)}, want ${JSON.stringify(subject)} - the marker is on the row and not in front of the reader`,
      list,
    );
  }

  // And it still says who WROTE it. Both have to be on the row: an entry that
  // named only the subject would be the impersonation this is meant to prevent,
  // and one that named only the writer would lose whose work it reports on.
  const wrote = await vouchedRow.first().locator("[data-worklog-actor]").getAttribute("title");
  if (wrote !== writer) {
    await die(
      `the vouched entry says it was written by ${JSON.stringify(wrote)}, want ${JSON.stringify(writer)}`,
      list,
    );
  }
  if (!(await vouchedRow.first().innerText()).toLowerCase().includes("vouched")) {
    await die("the vouched row says nothing a person reading it would see", list);
  }

  // The discriminating half: an ordinary entry is not marked. Everything above
  // passes with a view that badges every row.
  const authoredRow = rowFor(authored);
  if ((await authoredRow.count()) === 0) {
    await die(`the authored entry ${JSON.stringify(authored)} is not on the page`, list);
  }
  if ((await authoredRow.first().getAttribute("data-vouched")) !== null) {
    await die(
      `the entry ${JSON.stringify(authored)} is its own author's account and the page marks it vouched - a badge that is always on is a badge nobody reads`,
      list,
    );
  }

  console.log(
    `/worklog: ${subject}'s work, vouched for by ${writer}, drawn as vouched in a browser`,
  );
} finally {
  await browser.close();
}
