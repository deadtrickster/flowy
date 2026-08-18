/**
 * THE BOARD IS EVERY ROOM AND THE ROOM'S PANEL IS THE FILTER - asserted as a
 * DIFFERENCE, in a real browser, on two rows that differ only in whether they
 * carry a room.
 *
 *   node scripts/roomless-check.mjs BASE_URL TOKEN ROOM ROOMED_TITLE ROOMLESS_TITLE
 *
 * ROOMED_TITLE was raised in ROOM, so it carries fields.room. ROOMLESS_TITLE was
 * filed through POST /api/artifacts, which sets no room - the door most agent
 * rows go through, and the reason 22 of 46 open todos on the live node carried
 * none.
 *
 * Four claims, and the shape of them is the point. A check that asserted "the
 * board renders rows" would have passed all afternoon while the operator read 24
 * and the API answered 46, so every claim here is two readings that must differ:
 *
 *   - BOTH ROWS ARE ON /todos. The roomless one is the half that has nowhere
 *     else to appear: it is in no room's panel, so a board that narrowed by room
 *     would be narrowing away the only rows it is the only surface for.
 *   - THE TWO ROWS READ DIFFERENTLY IN THE SAME PLACE. The roomed row's cell
 *     says #ROOM and links to that room. The roomless row's cell says so in
 *     words. Not a blank - a blank reads as a room whose name did not load,
 *     which is neither of the two facts a reader has to tell apart: "filed
 *     nowhere" is a defect at the create door and "filed in general" is
 *     ordinary.
 *   - THE PAGE COUNTS THEM, and the number is the node's number rather than any
 *     number. A count that is right about nothing is how a filter that removes
 *     rows silently gets believed.
 *   - AND THE SAME TWO ROWS, ON THE ROOM'S OWN PANEL, ARE ONE ROW. That is the
 *     second arm: it proves the board is unfiltered rather than proving that
 *     nothing filters anywhere. One reading cannot tell those apart.
 *
 * The node is asked first, for both arms, because an absence assertion against a
 * store that never had the row passes against a page that renders nothing at
 * all. Everything in a room is read inside the PANEL and never off the page:
 * raising a todo in a room puts its title in the transcript too, so a page-text
 * search finds it there with the panel entirely absent.
 */

import { chromium } from "playwright";

const [base, token, room, roomedTitle, roomlessTitle] = process.argv.slice(2);
if (!base || !token || !room || !roomedTitle || !roomlessTitle) {
  console.error(
    "usage: node scripts/roomless-check.mjs BASE_URL TOKEN ROOM ROOMED_TITLE ROOMLESS_TITLE",
  );
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe page shows:\n${shown}` : message);
  process.exit(1);
}

/** ask reads one artifacts query back, or fails the check naming the query. */
async function ask(query) {
  const answer = await fetch(`${base}/api/artifacts?${query}`, { headers: bearer });
  if (!answer.ok) {
    die(`the node refused ?${query}: ${answer.status}`);
  }
  const { artifacts = [] } = await answer.json();
  return artifacts;
}

// ---------------------------------------------------------------- the node

// The wide read: no room asked for, which is the read the board makes.
const wide = await ask("type=memory&kind=todo&limit=1000");
const roomed = wide.find((a) => a.title === roomedTitle);
const nowhere = wide.find((a) => a.title === roomlessTitle);
const roomOf = (a) => (a?.fields && typeof a.fields.room === "string" ? a.fields.room : "");

if (!roomed) {
  die(`the node does not hand back ${JSON.stringify(roomedTitle)} at all - the seed is wrong`);
}
if (roomOf(roomed) !== room) {
  die(`${JSON.stringify(roomedTitle)} carries room ${JSON.stringify(roomOf(roomed))} and the check
was told it was raised in #${room} - the seed is wrong`);
}
if (!nowhere) {
  die(`the node does not hand back ${JSON.stringify(roomlessTitle)} on a read that asks for no
room. That is the defect this check is about and it is at the DOOR, not on the
page - nothing below could have been learned from a browser`);
}
if (roomOf(nowhere) !== "") {
  die(`${JSON.stringify(roomlessTitle)} carries room ${JSON.stringify(roomOf(nowhere))}, so the two
rows do not differ in the one way this check varies - the seed is wrong, or
something backfilled a room onto a row that was filed without one`);
}

// The narrow read, which is what the room's panel asks. Same door, one
// parameter different, and the answers have to differ: without this, "the board
// shows both" is equally true of a node that narrows nothing anywhere.
const narrow = await ask(`type=memory&kind=todo&room=${encodeURIComponent(room)}&limit=1000`);
if (!narrow.some((a) => a.title === roomedTitle)) {
  die(`?room=${room} does not hand back ${JSON.stringify(roomedTitle)}, which was raised there`);
}
if (narrow.some((a) => a.title === roomlessTitle)) {
  die(`?room=${room} hands back ${JSON.stringify(roomlessTitle)}, which carries no room at all, so
the door is not narrowing and the panel below cannot be evidence of anything`);
}

// How many of the board's rows carry no room. The page has to say THIS number.
const roomlessRows = wide.filter((a) => roomOf(a) === "").length;

const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
  const page = await context.newPage();
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // ------------------------------------------------------------- the board

  await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="todos across projects"]');
  try {
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`/todos has no list: no ol[aria-label="todos across projects"].${errors}`);
  }

  /** The row of one todo, by title - a row of the BOARD, by its own attribute. */
  const row = (title) => list.locator("li[data-todo-row]").filter({ hasText: title }).first();

  for (const [title, why] of [
    [roomedTitle, `raised in #${room}`],
    [roomlessTitle, "filed with no room, and this board is the only surface it can ever appear on"],
  ]) {
    try {
      await row(title).waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      const shown = await list.innerText().catch(() => "");
      const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
      die(
        `/todos does not show ${JSON.stringify(title)} - ${why}. The node hands it over on the
same read this page makes, so the row is being dropped between the door and the
screen.${errors}`,
        shown,
      );
    }
  }

  /** What one row says where the room goes: the attribute, the words, the link. */
  async function roomCell(title) {
    const cell = row(title).locator("[data-todo-room]");
    if ((await cell.count()) === 0) {
      const shown = await row(title)
        .innerText()
        .catch(() => "");
      die(
        `the row for ${JSON.stringify(title)} draws nothing carrying data-todo-room, so where a
room would be there is a blank. A blank is not a reading: it is how "filed
nowhere" and "filed in a room whose name did not load" look identical`,
        shown,
      );
    }
    return {
      attr: await cell.first().getAttribute("data-todo-room"),
      text: (await cell.first().innerText()).trim(),
      href: await cell.first().getAttribute("href"),
    };
  }

  // The roomless row first, deliberately: it is the row this whole check is
  // about, so a build that draws a blank there fails naming THAT rather than
  // naming whatever the roomed row happens to be missing.
  const nowhereCell = await roomCell(roomlessTitle);
  const roomedCell = await roomCell(roomedTitle);

  // The difference, first and plainly: two rows that differ only in whether they
  // carry a room must not read the same.
  if (roomedCell.text === nowhereCell.text) {
    die(`both rows say ${JSON.stringify(roomedCell.text)} where the room goes, so the board cannot
tell a row filed in #${room} from a row filed nowhere`);
  }
  if (nowhereCell.text === "") {
    die(`the roomless row's cell is empty, which is the blank this check exists to refuse - the
absence of a room has to be legible as an absence, in words`);
  }
  if (roomedCell.attr !== room) {
    die(`the row raised in #${room} carries data-todo-room=${JSON.stringify(roomedCell.attr)}`);
  }
  if (nowhereCell.attr !== "") {
    die(`the row filed with no room carries data-todo-room=${JSON.stringify(nowhereCell.attr)},
which claims a room the node never recorded on it`);
  }
  if (!roomedCell.text.includes(room)) {
    die(`the row raised in #${room} says ${JSON.stringify(roomedCell.text)} where the room goes,
so a reader is not told which room it was`);
  }
  if (roomedCell.href !== `/chat/${encodeURIComponent(room)}`) {
    die(`the room on a row goes to ${JSON.stringify(roomedCell.href)} rather than to that room's
own page, so it is a label dressed as a control`);
  }

  // The count, and it is the node's count. A page that says "1 filed in no room"
  // over twenty-two of them is the same lie in smaller type.
  const counter = page.locator("[data-todo-roomless-count]");
  if ((await counter.count()) === 0) {
    const shown = await page
      .locator("[data-todo-scope]")
      .innerText()
      .catch(() => "");
    die(
      `the board draws no count of the rows filed in no room: nothing carries
data-todo-roomless-count. That number is the one that says the create door is
losing where a row came from, and it was on no screen anywhere`,
      shown,
    );
  }
  const said = Number(await counter.first().getAttribute("data-todo-roomless-count"));
  if (said !== roomlessRows) {
    die(`the board says ${said} row(s) are filed in no room and the node hands it ${roomlessRows}`);
  }
  const scopeText = (
    await page
      .locator("[data-todo-scope]")
      .innerText()
      .catch(() => "")
  ).trim();
  if (!/no room/i.test(scopeText)) {
    die(`the scope line does not mention rooms at all, so a reader is not told this board spans
every one of them: ${JSON.stringify(scopeText)}`);
  }

  // --------------------------------------------------------- the room's pane

  // Driven as the journey: the room is reached by clicking the row's own link,
  // which is also the only thing that proves that control goes anywhere.
  await row(roomedTitle).locator("[data-todo-room]").first().click();
  await page.waitForURL(`**/chat/${room}`, { timeout: 20_000 }).catch(() => {});
  if (!page.url().includes(`/chat/${room}`)) {
    die(`clicking #${room} on the board left the browser at ${page.url()}`);
  }

  const panel = page
    .locator("aside section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();
  try {
    await panel.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`#${room} has no todo panel: no aside section headed "todos".${errors}`);
  }
  // The roomed row first: it is the proof the panel's list has ARRIVED. Reading
  // "the roomless one is absent" off a panel that has not loaded passes against
  // every build there has ever been.
  try {
    await panel.locator("li").filter({ hasText: roomedTitle }).first().waitFor({
      state: "visible",
      timeout: 15_000,
    });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die(
      `#${room}'s panel never showed ${JSON.stringify(roomedTitle)}, which was raised in it, so
nothing can be concluded from what else is missing`,
      shown,
    );
  }
  if ((await panel.locator("li").filter({ hasText: roomlessTitle }).count()) > 0) {
    const shown = await panel.innerText().catch(() => "");
    die(
      `#${room}'s panel holds ${JSON.stringify(roomlessTitle)}, which carries no room. The pane is
supposed to be the surface that narrows; if it shows everything then the board
and the pane are one list and the distinction the board states is false`,
      shown,
    );
  }

  console.log(
    `/todos showed both rows and told them apart - ${JSON.stringify(roomedCell.text)} against ` +
      `${JSON.stringify(nowhereCell.text)} - said ${said} of its rows are filed in no room, and ` +
      `#${room}'s own panel held the roomed one and not the roomless one`,
  );
} finally {
  await browser.close();
}
