/**
 * An ack is on the screen, it says how many, and pressing it takes it back.
 *
 *   node scripts/reaction-check.mjs BASE_URL TOKEN ROOM ACKED_MESSAGE PLAIN_MESSAGE
 *
 * ACKED_MESSAGE already carries a reaction from another principal when this
 * runs; PLAIN_MESSAGE carries none and is the control ON THE SAME PAGE. A strip
 * that drew on every message is indistinguishable from one that works, from a
 * single screenshot, and the control is the message next to it rather than a
 * second run against a clean room.
 *
 * ASSERTED ON ELEMENTS, never on page text. An emoji appears in message bodies,
 * in whatever anybody is saying about reactions in the room, and in this
 * check's own fixtures - so `page has 👀` passes with nothing drawn. Every
 * claim below reads a data attribute keyed by message id.
 *
 * THE COUNT IS ASSERTED AND NOT ONLY THE PRESENCE. In a room of four seats the
 * number is the difference between one seat acking and all of them, and a strip
 * that drew a chip without a count would pass a check that only asked whether a
 * chip was there.
 */

import { chromium } from "playwright";

const [base, token, room, acked, plain] = process.argv.slice(2);
if (!base || !token || !room || !acked || !plain) {
  console.error(
    "usage: node scripts/reaction-check.mjs BASE_URL TOKEN ROOM ACKED_MESSAGE PLAIN_MESSAGE",
  );
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
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // THE WITNESS THAT THIS RUN MEASURED ANYTHING. A room that never painted
  // answers "no reaction" to every question below, which is also what a
  // completely broken strip answers.
  const messages = page.locator("[data-message]");
  await messages
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await messages.count()) < 2) {
    die(
      `${room} painted ${await messages.count()} messages - this needs the acked one and its control`,
    );
  }

  // 1. THE ACKED MESSAGE DRAWS IT, WITH A COUNT.
  const strip = page.locator(`[data-reactions="${acked}"]`);
  await strip
    .first()
    .waitFor({ timeout: 10_000 })
    .catch(() => {});
  if ((await strip.count()) === 0) {
    die(
      `message ${acked} carries a reaction the node answers with and the room draws nothing - the ack is in the API and not on the screen, which is where it has to be`,
    );
  }
  const chip = page.locator(`[data-reaction^="${acked}:"]`).first();
  const count = await chip.getAttribute("data-reaction-count");
  if (count !== "1") {
    die(`the chip on ${acked} says ${count}, and one principal acked it`);
  }

  // 2. AND THE ONE BESIDE IT DOES NOT. This fails for a surface that draws a
  // strip on every message, which is the failure one screenshot cannot see.
  if ((await page.locator(`[data-reactions="${plain}"]`).count()) > 0) {
    die(`message ${plain} has no reactions and the room draws a strip on it anyway`);
  }

  // 3. PRESSING ADDS THIS READER'S, AND THE COUNT MOVES.
  //
  // Through the control a person actually uses rather than through the API: the
  // point of this arm is that the button is wired, and a check that POSTed and
  // then looked would pass with a button that does nothing.
  await page.locator(`[data-react-open="${acked}"]`).first().click();
  await page.locator(`[data-react-add="${acked}:👀"]`).first().click();
  const mine = page.locator(`[data-reaction="${acked}:👀"][data-reaction-mine="yes"]`);
  await mine
    .first()
    .waitFor({ timeout: 10_000 })
    .catch(() => {});
  if ((await mine.count()) === 0) {
    die(`pressing the control on ${acked} left no reaction of this reader's on it`);
  }
  const after = await page
    .locator(`[data-reaction="${acked}:👀"]`)
    .first()
    .getAttribute("data-reaction-count");
  if (after !== "2") {
    die(`after this reader acked, the chip says ${after} and two principals have acked it`);
  }

  // 4. AND PRESSING IT AGAIN TAKES ONLY THIS READER'S BACK.
  //
  // The other principal's ack surviving is what says a reaction is one
  // principal's word rather than a room-wide counter somebody can clear.
  await page.locator(`[data-reaction="${acked}:👀"]`).first().click();
  const back = page.locator(`[data-reaction="${acked}:👀"][data-reaction-mine="no"]`);
  await back
    .first()
    .waitFor({ timeout: 10_000 })
    .catch(() => {});
  if ((await back.count()) === 0) {
    die(`pressing this reader's own chip on ${acked} did not take it back`);
  }
  const left = await page
    .locator(`[data-reaction="${acked}:👀"]`)
    .first()
    .getAttribute("data-reaction-count");
  if (left !== "1") {
    die(`taking this reader's ack back left ${left} - the other principal's should still be on it`);
  }

  if (crashes.length > 0) {
    die(`the console threw while drawing reactions: ${crashes.join("; ")}`);
  }
  console.log(`${acked} acked by two, taken back to one, and ${plain} beside it draws nothing`);
} finally {
  await browser.close();
}
