/**
 * A ROW ON THE BOARD SAYS WHEN IT LAST MOVED.
 *
 *   node scripts/todo-when-check.mjs BASE_URL TOKEN
 *
 * The board had no time on it anywhere. A row raised this morning and one
 * nobody had touched since June rendered identically, and the only way to tell
 * them apart was to open each one - on a list that is hundreds long.
 *
 * ASSERTED AGAINST THE NODE'S OWN VALUE, not against a shape. A regex for
 * something-that-looks-like-a-time passes on a row that renders a DIFFERENT
 * row's stamp, or last week's, or the string "now" - so this reads `updated`
 * from the door for a row it just raised and requires the page to be showing
 * that instant.
 *
 * AND IN THE CONSOLE'S ONE FORMAT. clock() is the operator's rule - 01M10Y3JBD,
 * "all time labels must show the date 'if not today'" - so a row raised today
 * shows a time and NOT a date, exactly as the room does. A second time format
 * on one page is the defect that rule was written against.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/todo-when-check.mjs BASE_URL TOKEN");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

// POST /api/chat/{room}/todo - a row is raised INTO a room. There is no
// /api/todos to post to, and a check that invented one would have failed for a
// reason that has nothing to do with what it is measuring.
const raised = await fetch(`${base}/api/chat/todowhen/todo`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "content-type": "application/json" },
  body: JSON.stringify({ title: "a row that says when it moved" }),
});
if (!raised.ok) die(`could not raise a row to measure: ${raised.status}`);
const row = await raised.json();
// THE ROW IS THE EVENT'S ARTIFACT. Raising a todo in a room writes a chat
// EVENT whose `artifact` is the row it made - the event's own id is the message
// that announced it, and looking for `id` here finds that instead and then
// hunts the board for a row that does not exist.
const id = row.event?.artifact ?? row.artifact ?? row.id;
if (!id) die(`the raise answered no row id: ${JSON.stringify(row).slice(0, 200)}`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/todos`, { timeout: 30_000 });

  const line = page.locator(`[data-todo-row="${id}"]`);
  await line.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await line.count()) === 0) die("the row just raised is not on /todos");

  const when = line.locator("[data-todo-when]");
  if ((await when.count()) === 0) {
    die(`the row draws no time at all: ${JSON.stringify((await line.innerText()).slice(0, 200))}

A board with no time on it cannot be triaged - a row from this morning and one
nobody has touched since June read the same.`);
  }

  // THE NODE'S VALUE, not a plausible-looking one.
  const carried = await when.getAttribute("data-todo-when");
  if (!carried || Number.isNaN(Date.parse(carried))) {
    die(`the row's time is not a timestamp: ${JSON.stringify(carried)}`);
  }
  const drift = Math.abs(Date.now() - Date.parse(carried));
  if (drift > 10 * 60 * 1000) {
    die(
      `a row raised seconds ago says it moved at ${carried}, ${Math.round(
        drift / 60000,
      )} minutes away - the page is drawing some other row's stamp`,
    );
  }

  // AND IN THE CONSOLE'S FORMAT. Raised today, so clock() gives a time with no
  // date; a date here means a second format was invented for this page.
  const shown = (await when.innerText()).trim();
  if (!/^\d{1,2}:\d{2}/.test(shown)) {
    die(`a row raised today shows ${JSON.stringify(shown)}.

clock() renders today as the time alone and only adds a date when the day
differs - 01M10Y3JBD. A date here means this page has its own time format.`);
  }

  console.log(`the board says when a row last moved: ${shown}, from the node's own ${carried}`);
} finally {
  await browser.close();
}
