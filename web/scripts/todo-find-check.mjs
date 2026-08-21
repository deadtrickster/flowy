/**
 * The room's todo panel CAN BE SEARCHED, and it says what it is not showing.
 *
 *   node scripts/todo-find-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR, 06:47: "might be duplicates because of your stulls and the fact
 * that it is impossible to filter by author or seaech on the todo pane of room".
 * They were right about the consequence within the hour: two agents filed the
 * same row seconds apart that morning - 01M0HPQASA and 01M0HPPY7G, one closed as
 * the other's duplicate - each having decided independently that nothing covered
 * it, because neither could ask. 32 open rows and no way to look.
 *
 * FOUR CLAIMS, and the last two are the ones a naive implementation drops:
 *
 *   - a word narrows the list to the rows whose TITLE carries it;
 *   - "@somebody" narrows it to that person's rows instead of matching titles,
 *     which is the half of the ask a plain substring search does not answer;
 *   - the panel SAYS HOW MANY IT IS WITHHOLDING and says it for the right
 *     reason. A line reading "16 done hidden" while a search is on is a false
 *     statement about the queue, and this panel's whole failure mode is a reader
 *     concluding a row does not exist;
 *   - a search that MATCHES NOTHING says so, distinctly from a room that raised
 *     nothing and from a room that finished everything. Three empties, three
 *     questions - collapsing them is the same defect one surface over, and it is
 *     the reason somebody types into the box at all.
 *
 * The fixtures are raised through the API rather than the panel, so what is
 * asserted is the panel READING a queue it did not create.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/todo-find-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

// This check seeds fixtures, so it must not be aimed at a live node by accident.
refuseRemote(base, "todo-find-check");

const bearer = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };

function die(message, shown) {
  console.error(shown ? `${message}\nThe panel shows:\n${shown}` : message);
  process.exit(1);
}

const stamp = String(process.hrtime.bigint()).slice(-8);
const HERRING = `zzz herring ${stamp}`;
const WANTED = `qqq wanted ${stamp}`;
const RAISER = `finder${stamp}`;

async function raise(title, raiser) {
  const res = await fetch(`${base}/api/chat/${room}/todo`, {
    method: "POST",
    headers: bearer,
    body: JSON.stringify({ title, category: "chore", raiser }),
  });
  if (!res.ok) {
    console.error(`seeding ${JSON.stringify(title)} answered ${res.status}: ${await res.text()}`);
    process.exit(1);
  }
}

// The raiser is NOT defaulted to the caller - store/raiser.go: "Empty is the
// ordinary case and it means nobody said" - so it is stated here. It is a name
// that appears in NO title, which is what makes the people arm unforgeable: a
// box that only ever matched titles cannot pass this by accident.
await raise(HERRING, "someone-else");
await raise(WANTED, RAISER);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  const panel = page
    .locator("aside section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();
  try {
    await panel.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the room has no todo panel: no aside section headed "todos".${errors}`);
  }

  const box = panel.locator("[data-todo-find]");
  try {
    await box.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die(
      `the todo panel has nothing carrying data-todo-find, so there is no way to
ask whether a row has already been raised - which is the whole of this row`,
      shown,
    );
  }

  const rows = () => panel.locator("li");
  const rowFor = (title) => panel.locator("li").filter({ hasText: title });

  // BOTH ARE THERE BEFORE ANYTHING IS TYPED, so a later disappearance is the
  // filter working rather than a row that never arrived.
  for (const title of [HERRING, WANTED]) {
    try {
      await rowFor(title).first().waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      die(`the seeded row ${JSON.stringify(title)} never reached the panel`, shown);
    }
  }
  const before = await rows().count();

  // A WORD NARROWS BY TITLE.
  await box.fill("qqq");
  try {
    await rowFor(HERRING).first().waitFor({ state: "hidden", timeout: 10_000 });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die(
      `typing "qqq" left ${JSON.stringify(HERRING)} on screen, so the box is not filtering
by title - a search that shows everything answers nothing`,
      shown,
    );
  }
  if ((await rowFor(WANTED).count()) === 0) {
    const shown = await panel.innerText().catch(() => "");
    die(`typing "qqq" also hid ${JSON.stringify(WANTED)}, which is the row it names`, shown);
  }

  // AND IT SAYS WHAT IT IS WITHHOLDING, for the search rather than for done.
  const withheld = panel.locator("[data-hidden-count]");
  if ((await withheld.count()) === 0) {
    const shown = await panel.innerText().catch(() => "");
    die(
      `${before - 1} row(s) are being withheld and the panel says nothing about them.
A panel showing one row with no sign of the rest lies about the size of the queue`,
      shown,
    );
  }
  const said = (await withheld.innerText()).trim();
  if (!/not matching/.test(said)) {
    const shown = await panel.innerText().catch(() => "");
    die(
      `while searching, the panel accounts for the missing rows as ${JSON.stringify(said)}.
Rows hidden by a search are not rows hidden because they are done, and saying
the second about the first is a false statement about the queue`,
      shown,
    );
  }

  // "@somebody" ASKS ABOUT PEOPLE, NOT TITLES, which is the half of the ask a
  // plain substring search does not answer. RAISER appears in no title, so a box
  // that only ever matched titles cannot pass this by accident - it would hide
  // both rows and fail on the first assertion.
  await box.fill(`@${RAISER}`);
  try {
    await rowFor(WANTED).first().waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die(
      `"@${RAISER}" hid ${JSON.stringify(WANTED)}, the row raised by exactly that name.
The people arm either is not there, or it is matching titles - and no title here
carries that string`,
      shown,
    );
  }
  // AND IT IS A FILTER RATHER THAN A NO-OP. Without this, a box that ignored
  // "@" entirely and showed everything would pass the assertion above.
  if ((await rowFor(HERRING).count()) > 0) {
    const shown = await panel.innerText().catch(() => "");
    die(
      `"@${RAISER}" left ${JSON.stringify(HERRING)} on screen, which somebody else raised.
The people arm is not narrowing anything`,
      shown,
    );
  }

  // A SEARCH THAT MATCHES NOTHING SAYS SO, and says something a reader can tell
  // apart from an empty room.
  await box.fill(`nothing-matches-${stamp}`);
  try {
    await rows().first().waitFor({ state: "hidden", timeout: 10_000 });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die("a search for a string nothing carries still drew rows", shown);
  }
  const empty = (await panel.innerText()).trim();
  if (!/matches/.test(empty)) {
    die(
      `with a search matching nothing, the panel says:\n${empty}\n
which does not say that the SEARCH is why it is empty. A reader cannot tell it
from a room that raised nothing, and concludes the row does not exist - which is
how the duplicate that prompted this got filed`,
    );
  }

  console.log("ok: the todo panel searches by word and by person, and says what it withholds");
} finally {
  await browser.close();
}
