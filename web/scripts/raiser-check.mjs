/**
 * A queue row says BOTH who raised the work and who is carrying it, in a real
 * browser, on the elements.
 *
 *   node scripts/raiser-check.mjs BASE_URL TOKEN PROJECT TODO_ID TITLE RAISER CARRIER
 *
 * The two names are the point. `owner_user` is the seat whose token wrote the
 * row - for a board four agents file into, that is the agent that typed it and
 * not the party the work came from - so a row that draws one name leaves a
 * reader unable to tell an agent's own idea from somebody's request. "Raised by
 * X, carried by Y" is two facts, and this check fails unless the screen carries
 * both of them, on the SAME row, with the values the node holds.
 *
 * Three claims, in the order they would break:
 *
 *   - the queue page draws the raiser and the carrier on the row, and they are
 *     not the same name. A check whose two names were equal would pass against
 *     a page that drew one of them twice;
 *   - the artifact page says both in words, because that is the page somebody
 *     opens when the row is not enough;
 *   - and both match the NODE. A name that is right on this screen and absent
 *     from the store is the same bug one reload later, and a console that read
 *     the raiser off the wrong key would draw a blank exactly where a row that
 *     genuinely says nothing draws one.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, project, id, title, raiser, carrier] = process.argv.slice(2);
if (!base || !token || !project || !id || !title || !raiser || !carrier) {
  console.error(
    "usage: node scripts/raiser-check.mjs BASE_URL TOKEN PROJECT TODO_ID TITLE RAISER CARRIER",
  );
  process.exit(2);
}
if (raiser === carrier) {
  console.error(
    `the raiser and the carrier are both ${JSON.stringify(raiser)}, so this check could not
tell a row that draws two facts from one that draws one of them twice`,
  );
  process.exit(2);
}

refuseRemote(base, "raiser-check");

const bearer = { Authorization: `Bearer ${token}` };

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe screen shows:\n${shown}` : message);
  process.exit(1);
}

// The node first, because everything below is an assertion that the screen
// agrees with it. If the store does not hold these two names then the check is
// about the seed rather than about the console, and it should say so.
const held = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, { headers: bearer });
if (!held.ok) {
  die(`the node does not answer for todo ${id}: ${held.status}`);
}
const item = await held.json();
if (item.raiser !== raiser || item.assignee !== carrier) {
  die(`the node has raiser ${JSON.stringify(item.raiser)} and assignee ${JSON.stringify(
    item.assignee,
  )} on ${id}, and this check was told ${JSON.stringify(raiser)} and ${JSON.stringify(carrier)}:
nothing about the console was tested`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // ------------------------------------------------------------ the queue row
  await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});
  const row = page.locator(`li[data-todo-row="${id}"]`);
  try {
    await row.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the queue page has no row for ${id} (${JSON.stringify(title)}), so there is nothing
to say who raised it.${errors}`);
  }

  const raisedCell = row.locator("[data-todo-raiser]");
  if ((await raisedCell.count()) === 0) {
    die(
      `the row for ${JSON.stringify(title)} carries nothing with data-todo-raiser: the queue
draws who is carrying the work and not where it came from, which is the ambiguity
this field exists to end`,
      await row.innerText().catch(() => ""),
    );
  }
  const saysRaised = (await raisedCell.first().innerText()).trim();
  if (!saysRaised.includes(raiser)) {
    die(
      `the row says ${JSON.stringify(saysRaised)} where it should name ${JSON.stringify(raiser)}
as the party the work came from`,
      await row.innerText().catch(() => ""),
    );
  }
  const carriedCell = row.locator("[data-todo-assignee]");
  if ((await carriedCell.count()) === 0) {
    die(
      `the row for ${JSON.stringify(title)} carries nothing with data-todo-assignee: the raiser
displaced who is carrying the work rather than sitting beside it`,
      await row.innerText().catch(() => ""),
    );
  }
  const saysCarried = (await carriedCell.first().innerText()).trim();
  if (!saysCarried.includes(carrier)) {
    die(
      `the row says ${JSON.stringify(saysCarried)} is carrying it, want ${JSON.stringify(carrier)}`,
      await row.innerText().catch(() => ""),
    );
  }

  // --------------------------------------------------------- the artifact page
  await page
    .goto(`${base}/p/${encodeURIComponent(project)}/memory/${encodeURIComponent(id)}`, {
      timeout: 20_000,
    })
    .catch(() => {});
  const pair = page.locator("[data-artifact-raiser]");
  try {
    await pair.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(
      `the artifact page for ${id} says nothing about who raised it.${errors}`,
      await page
        .locator("body")
        .innerText()
        .catch(() => ""),
    );
  }
  const raisedLine = (await pair.first().innerText()).trim();
  const carriedLine = (await page.locator("[data-artifact-assignee]").first().innerText()).trim();
  if (!raisedLine.includes(raiser) || !raisedLine.toLowerCase().includes("raised by")) {
    die(`the artifact page says ${JSON.stringify(raisedLine)} rather than that it was raised
by ${JSON.stringify(raiser)}`);
  }
  if (!carriedLine.includes(carrier) || !carriedLine.toLowerCase().includes("carried by")) {
    die(`the artifact page says ${JSON.stringify(carriedLine)} rather than that it is carried
by ${JSON.stringify(carrier)}`);
  }

  console.log(
    `the queue row and the artifact page for ${JSON.stringify(
      title,
    )} both say raised by ${raiser}, carried by ${carrier}`,
  );
} finally {
  await browser.close();
}
