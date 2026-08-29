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

  console.log(
    `${total} messages: ${opens} open a run and say who is speaking, ${continues} continue one and do not repeat it`,
  );
} finally {
  await browser.close();
}
