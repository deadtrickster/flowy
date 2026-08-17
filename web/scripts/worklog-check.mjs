/**
 * The worklog, on the page, in a real browser, asserted on ELEMENTS.
 *
 *   node scripts/worklog-check.mjs BASE_URL TOKEN NEWEST_WHAT BRANCH OTHER_WHAT ACTOR
 *
 * The worklog is the fleet's memory across sessions and had no human surface at
 * all until this page: written and read over MCP, so the one thing whose whole
 * purpose is "what happened and what is next" could only be reached by an agent
 * holding an MCP client. What this checks is that a person can now read it.
 *
 * Elements rather than page text, for the reason browser-check.mjs already
 * learned the hard way: "worklog" is in the global navigation, so a page-text
 * search for it passes with the list entirely absent. So this finds the LIST,
 * reads its ROWS, and takes the branch off each row's own attribute.
 *
 * Five claims, and the last is the one that discriminates:
 *
 *   - the list is there and has rows                 - the page renders entries
 *   - the newest seeded entry is the FIRST row       - newest first
 *   - that row names the seat that wrote it          - who, per entry
 *   - that row carries the branch it was written on  - where, per entry
 *   - narrowing to a branch keeps it and drops the   - the branch is a FILTER
 *     entry written on another branch
 *
 * Without the last, a filter that narrows nothing passes every other assertion
 * here: the entry it was meant to keep is on the page either way.
 */

import { chromium } from "playwright";

const [base, token, newest, branch, other, actor] = process.argv.slice(2);

if (!base || !token || !newest || !branch || !other || !actor) {
  console.error(
    "usage: node scripts/worklog-check.mjs BASE_URL TOKEN NEWEST_WHAT BRANCH OTHER_WHAT ACTOR",
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
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `/worklog has no entry list: no ol[aria-label="worklog entries"].
The word "worklog" is in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }

  // Wait for a ROW, not for the list. The list paints from mount with its empty
  // state in it and the entries arrive one fetch later, so reading it the
  // moment it appears asserts on the empty state and fails a page that works.
  try {
    await rows.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die("/worklog rendered the list and no entries at all", list);
  }

  // Newest first, because the question a worklog answers is what just happened.
  const first = (await rows.first().locator("[data-worklog-what]").innerText()).trim();
  if (!first.includes(newest)) {
    await die(
      `the first entry is ${JSON.stringify(first)}, want the newest one ${JSON.stringify(newest)}`,
      list,
    );
  }

  // Which seat wrote it. The cell draws the name the node stamped and keeps the
  // id on its title, so the id is what this asserts on: a name is a display
  // choice and the id is the attribution, and "which seat wrote this" is the
  // first thing the next one asks.
  const wrote = await rows.first().locator("[data-worklog-actor]").getAttribute("title");
  if (wrote !== actor) {
    await die(
      `the newest entry says it was written by ${JSON.stringify(wrote)}, want ${JSON.stringify(actor)}`,
      list,
    );
  }

  // The branch is on the ROW, read off its own attribute rather than found
  // somewhere in the page's text - a string that appears in two places is not
  // evidence about either.
  const firstBranch = await rows.first().getAttribute("data-branch");
  if (firstBranch !== branch) {
    await die(
      `the newest entry says its branch is ${JSON.stringify(firstBranch)}, want ${JSON.stringify(branch)}`,
      list,
    );
  }

  // Default is everything: the entry written on the OTHER branch is here too. A
  // worklog scoped to one branch by default hides the work somebody else did.
  const otherRow = rows.filter({ hasText: other });
  if ((await otherRow.count()) === 0) {
    await die(
      `unnarrowed, the worklog does not show ${JSON.stringify(other)} - it defaults to one branch rather than to everything`,
      list,
    );
  }

  // And now the filter. This is the discriminating half: everything above
  // passes with a picker that narrows nothing.
  const picker = page.locator('select[aria-label="branch"]');
  const options = await picker.locator("option").evaluateAll((nodes) => nodes.map((n) => n.value));
  if (!options.includes(branch)) {
    await die(`the branch picker offers ${JSON.stringify(options)}, with no ${branch} in it`, list);
  }
  await picker.selectOption(branch);

  try {
    await page.waitForFunction(
      (name) => {
        const shown = [...document.querySelectorAll("li[data-worklog-entry]")];
        return shown.length > 0 && shown.every((li) => li.getAttribute("data-branch") === name);
      },
      branch,
      { timeout: 10_000 },
    );
  } catch {
    const branches = await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getAttribute("data-branch")),
    );
    await die(
      `narrowed to ${branch}, the list still holds entries from ${JSON.stringify([...new Set(branches)])}`,
      list,
    );
  }

  if ((await rows.filter({ hasText: newest }).count()) === 0) {
    await die(`narrowing to ${branch} dropped the entry written on it`, list);
  }
  if ((await rows.filter({ hasText: other }).count()) > 0) {
    await die(
      `narrowing to ${branch} kept ${JSON.stringify(other)}, written on another branch`,
      list,
    );
  }

  console.log(
    `/worklog: ${await rows.count()} entr(y|ies) on ${branch}, newest first, in a browser`,
  );
} finally {
  await browser.close();
}
