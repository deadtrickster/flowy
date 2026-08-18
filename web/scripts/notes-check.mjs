/**
 * A row RENDERS ITS NOTES UNDER THE BODY, and a person can add one. In a real
 * browser, on the elements.
 *
 *   node scripts/notes-check.mjs BASE_URL TOKEN TODO_ID BODY NOTE
 *
 * BODY is the words the row was filed with and NOTE is what this check writes
 * through the console's own box.
 *
 * The store half of this landed with the doors and is checked beside them. What
 * is checked here is the half a reader actually has: until the page draws them,
 * what was learned about a row is in the log and invisible, which is the state
 * that had one defect diagnosed four times by four agents in an evening.
 *
 * Four claims, in the order they would break:
 *
 *   - every note the node holds is ON THE PAGE, in its own words. A page that
 *     drew "3 notes" and no text would leave the reader exactly where they were;
 *   - each one says WHO wrote it. An agent's measurement and its operator's
 *     instruction are read differently, and a wall of unattributed paragraphs
 *     under somebody's body is not a record;
 *   - they are UNDER the body and in the node's order, oldest first. The author's
 *     statement of the work comes first or the reader is reading answers before
 *     the question;
 *   - and the box WRITES, to the node. A control that puts a note in the tab and
 *     nowhere else is the same empty row one reload later.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, id, body, note] = process.argv.slice(2);
if (!base || !token || !id || !body || !note) {
  console.error("usage: node scripts/notes-check.mjs BASE_URL TOKEN TODO_ID BODY NOTE");
  process.exit(2);
}

// This check writes a note through the console, and a note cannot be deleted.
refuseRemote(base, "notes-check");

const bearer = { Authorization: `Bearer ${token}` };

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe page shows:\n${shown}` : message);
  process.exit(1);
}

/** What the node holds on the row right now, through the same read the page does. */
async function held() {
  const answer = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, { headers: bearer });
  if (!answer.ok) {
    die(`the node does not answer for todo ${id}: ${answer.status}`);
  }
  return answer.json();
}

// The node first: everything below asserts the screen agrees with it, so a row
// the store has no notes on would make this a check about the seed.
const item = await held();
const notes = item.notes ?? [];
if (notes.length < 2) {
  die(`the node holds ${notes.length} note(s) on ${id}, and this check needs at least two to say
anything about the order they are drawn in: nothing about the console was tested`);
}
if (item.body !== body) {
  die(`the node has ${JSON.stringify(item.body)} as the body of ${id}, and this check was told
${JSON.stringify(body)}`);
}

const project = item.project;
if (!project) {
  die(`todo ${id} has no project, so it has no page in the console - and the node refuses a note
on it for the same reason`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  await page
    .goto(`${base}/p/${encodeURIComponent(project)}/memory/${encodeURIComponent(id)}`, {
      timeout: 20_000,
    })
    .catch(() => {});

  const section = page.locator("[data-row-notes]");
  try {
    await section.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(
      `the page for ${id} has nothing with data-row-notes: the node holds ${notes.length} notes on
this row and the console draws none of them, which is the whole of what a reader
gets out of this feature.${errors}`,
      await page
        .locator("body")
        .innerText()
        .catch(() => ""),
    );
  }

  /** The words of every note on screen, in the order they are drawn. */
  const drawn = async () => {
    const entries = await page.locator("[data-row-note]").all();
    return Promise.all(entries.map((entry) => entry.innerText()));
  };

  // Every one of them, in its own words, and each attributed to the seat the
  // node says wrote it.
  const shown = await drawn();
  if (shown.length !== notes.length) {
    die(
      `the node holds ${notes.length} notes on ${id} and the page draws ${shown.length}`,
      (await section.innerText()) || "",
    );
  }
  for (const [at, entry] of notes.entries()) {
    if (!shown[at].includes(entry.note)) {
      die(
        `note ${at + 1} on the page says ${JSON.stringify(shown[at])} where the node holds
${JSON.stringify(entry.note)} - the notes are drawn in an order that is not the node's, or one of
them is not on the page at all`,
        (await section.innerText()) || "",
      );
    }
    const seat = page.locator(`[data-row-note] [data-note-actor="${entry.actor}"]`);
    if ((await seat.count()) === 0) {
      die(
        `nothing on the page names ${entry.actor} as the writer of ${JSON.stringify(entry.note)},
so the page draws a paragraph under somebody else's body with nobody's name on it`,
        (await section.innerText()) || "",
      );
    }
  }

  // UNDER the body, which is what makes this a row somebody can read top to
  // bottom. Document order, so it holds however the section is laid out.
  const under = await page.evaluate(() => {
    const said = document.querySelector("[data-artifact-body]");
    const learned = document.querySelector("[data-row-notes]");
    if (!said || !learned) return null;
    return Boolean(said.compareDocumentPosition(learned) & Node.DOCUMENT_POSITION_FOLLOWING);
  });
  if (under === null) {
    die("the page draws no body, so there is nothing for the notes to be under");
  }
  if (!under) {
    die(`the notes are drawn BEFORE the body of ${id}: a reader meets what four other people
learned about the work before the statement of what the work is`);
  }

  // And the box writes. Through the control a person drives, and asserted at the
  // node afterwards - a note held in this tab is the same missing note one
  // reload later.
  const draft = page.locator("[data-note-draft]");
  if ((await draft.count()) === 0) {
    die(
      `the page draws the notes and offers no way to add one, so what somebody learns here still
has to go into a room`,
      (await section.innerText()) || "",
    );
  }
  await draft.fill(note);
  await page.locator("[data-note-add]").click();

  try {
    await page
      .locator("[data-row-note]", { hasText: note })
      .first()
      .waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(
      `the console took ${JSON.stringify(note)} and never drew it, so somebody who wrote a note
cannot tell whether it landed.${errors}`,
      (await section.innerText()) || "",
    );
  }

  const after = await held();
  const stored = (after.notes ?? []).map((entry) => entry.note);
  if (!stored.includes(note)) {
    die(`the page shows ${JSON.stringify(note)} and the node holds ${stored.length} notes without
it: the box wrote nowhere`);
  }
  if (stored.length !== notes.length + 1) {
    die(`the row had ${notes.length} notes and now has ${stored.length}: adding one changed
something else about the row`);
  }
  // The author's own words are untouched, which is the whole difference between
  // this and the edit door. A console that "added" a note by rewriting the body
  // would pass every assertion above.
  if (after.body !== body) {
    die(`the body of ${id} is now ${JSON.stringify(after.body)} and was ${JSON.stringify(body)}:
appending a note rewrote the author's words`);
  }

  console.log(
    `the page drew ${notes.length} notes under the body, each with its writer, and the box added a ${
      notes.length + 1
    }th that the node holds`,
  );
} finally {
  await browser.close();
}
