/**
 * A run of one speaker says who is speaking ONCE, and every break says it again.
 *
 *   node scripts/message-runs-check.mjs BASE_URL TOKEN ROOM
 *
 * The room repeated the whole identity cluster - agent/human badge, name,
 * authorship mark, private and addressed marks - on every message, so four
 * consecutive lines from one seat said the same four things four times. The
 * reader takes them in once and then re-reads them per row.
 *
 * COUNTED, NOT EYEBALLED. "looks grouped" is not a measurement, so every arm
 * here counts elements: how many rows OPEN a run against how many rows exist.
 * A surface that drew no headers at all would satisfy any assertion phrased as
 * "fewer headers than messages", which is why the first row of every run is
 * asserted to keep its own.
 *
 * AND THE BREAKS ARE THE POINT. Grouping is only safe if it stops at every
 * thing the header exists to say. The one that matters most is AUTHORSHIP:
 * MessageList's own note says attributed means this node could not verify a
 * signature of the speaker's own, so the message rests on the word of whichever
 * node relayed it - a pinned peer could otherwise write under anybody's name.
 * Grouping across a change in that mark would hide the single transition it
 * exists to show, which is a worse defect than the repetition it removes. That
 * arm is asserted in internal/flowy against the store, where the two authorship
 * values can be made to order; here the break asserted is the SPEAKER, which is
 * the one a room can produce honestly.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/message-runs-check.mjs BASE_URL TOKEN ROOM");
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
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });

  const rows = page.locator("[data-message]");
  await rows
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  const total = await rows.count();
  if (total < 4) {
    die(`the room drew ${total} messages and this needs at least four to see a run`);
  }

  const opens = await page.locator('[data-msg-header="opens"]').count();
  const continues = await page.locator('[data-msg-header="continues"]').count();

  // EVERY ROW SAYS WHICH IT IS. A row with neither attribute is a row this
  // check cannot speak about, and a silent gap is how a count starts lying.
  if (opens + continues !== total) {
    die(
      `${total} messages but ${opens} open and ${continues} continue - ${
        total - opens - continues
      } row(s) say neither, so the count below would be about the wrong set`,
    );
  }

  // A HEADER IS NOT DRAWN ON EVERY ROW ANY MORE. This is the change.
  if (continues === 0) {
    die(`all ${total} messages open their own run, so nothing is grouped. The fixture
posts several in a row from one seat, so at least one must continue.`);
  }

  // AND NOT NONE EITHER. A surface that stopped drawing headers would pass the
  // arm above and be a room where nobody can tell who spoke.
  if (opens === 0) {
    die(`${total} messages and not one opens a run - no message says who is speaking`);
  }

  // THE FIRST ROW ON SCREEN ALWAYS OPENS ONE. There is nothing above it to
  // continue from, so a "continues" there means the run was computed against a
  // message that is not drawn.
  const firstIs = await rows.first().locator("[data-msg-header]").getAttribute("data-msg-header");
  if (firstIs !== "opens") {
    die(`the first message on screen says ${JSON.stringify(firstIs)} - it has nothing above it
to continue from, so the run was computed against a row nobody can see`);
  }

  // AND THE ATTRIBUTE HAS TO MATCH WHAT IS DRAWN.
  //
  // Everything above counts data-msg-header, which this component sets from the
  // same expression that decides the header - so all of it passes if the two
  // ever disagree. Proven: with the conditional forced open, every row drew a
  // full header and every count above stayed green.
  //
  // So this reads the header ELEMENT. A row that continues a run must not draw
  // the agent/human badge; a row that opens one must. The badge word is the
  // thing being deduplicated, and it is inside the header rather than the body,
  // so this cannot pass on prose that happens to mention it.
  const says = async (which) =>
    await page.locator(`[data-msg-header="${which}"]`).first().innerText();
  const opener = (await says("opens")).trim();
  if (!/\b(agent|human)\b/.test(opener)) {
    die(`a message that opens a run draws no speaker badge: ${JSON.stringify(opener)}`);
  }
  const continuer = (await says("continues")).trim();
  if (/\b(agent|human)\b/.test(continuer)) {
    die(`a message that CONTINUES a run still draws the speaker badge: ${JSON.stringify(continuer)}

The attribute says it is grouped and the pixels say it is not, which means the
counts above are describing something other than the page.`);
  }

  // AND A RUN LOOKS LIKE ONE.
  //
  // Everything above was green on a version the operator called broken on
  // sight: "where is the author of the secodn message?? why all this space".
  // Drawing the header once is only half the change - the continuation kept its
  // own border and an empty header row with the time pushed to the right, so it
  // read as a message from nobody with a blank line on top. Grouping the
  // identity while leaving the boxes apart is worse than not grouping at all.
  //
  // MEASURED AS GEOMETRY, because "reads as one block" is not a class. Two
  // numbers, and each is a thing a person complained about:
  //
  //   the gap between a continuation and the row above it, against the gap
  //   between two rows that each open a run. A run must be tighter.
  //
  //   the height of the header on a continuation, which carries only a
  //   timestamp and must not cost a whole row.
  const boxOf = async (which, index) => {
    const rows = page.locator(`[data-message]:has([data-msg-header="${which}"])`);
    return await rows.nth(index).boundingBox();
  };
  const firstOpen = await boxOf("opens", 0);
  const firstCont = await boxOf("continues", 0);
  if (!firstOpen || !firstCont) die("could not measure a run against a break");

  // The continuation's top edge against the bottom edge of whatever is above
  // it: rows are in document order, so the row above the first continuation is
  // the head of its run.
  const gap = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("[data-message]")];
    const i = rows.findIndex((r) => r.querySelector('[data-msg-header="continues"]'));
    if (i < 1) return null;
    const above = rows[i - 1].getBoundingClientRect();
    const here = rows[i].getBoundingClientRect();
    const header = rows[i].querySelector('[data-msg-header="continues"]');
    return {
      gap: Math.round(here.top - above.bottom),
      headerHeight: Math.round(header ? header.getBoundingClientRect().height : 0),
      bodyHeight: Math.round(here.height),
    };
  });
  if (!gap) die("no continuation with a row above it to measure against");

  if (gap.gap > 4) {
    die(`a message continuing a run sits ${gap.gap}px below the one above it.

A run has to READ as one block. Grouping the header while leaving the rows as
separate boxes is what the operator saw: "where is the author of the secodn
message?? why all this space".`);
  }
  if (gap.headerHeight > gap.bodyHeight / 2) {
    die(`the header on a continuing message is ${gap.headerHeight}px of a ${gap.bodyHeight}px row.

It carries only a timestamp. A whole empty row for it is the space that was
reported - the identity was removed and the row it sat in was not.`);
  }

  console.log(
    `${total} messages: ${opens} open a run and say who is speaking, ${continues} continue one and do not repeat it; a continuation sits ${gap.gap}px below its head`,
  );
} finally {
  await browser.close();
}
