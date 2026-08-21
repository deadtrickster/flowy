/**
 * The transcript says a message has replies, how many, and gets you to them.
 *
 *   node scripts/thread-count-check.mjs BASE_URL TOKEN
 *
 * The operator, 2026-08-20: "i also miss normal slack/mattermost style threads.
 * like why didnt you reply to my plans proposal in a thread. impossible to track
 * things here." Measured before building: the mechanism was never what was
 * missing. Events carry a thread, `flowy say --thread` continues one, and the
 * console has had a pane that draws one all along - and in the last 40 messages
 * of #general there were 40 messages, 40 distinct threads, and none with more
 * than one message in it. Four seats talked past each other for a day in a room
 * that already did the thing they wanted.
 *
 * THE NUMBER IS THE NODE'S, AND THAT IS WHAT THIS CHECKS. The console holds a
 * window of the room - sixty messages - so a count folded from what is on
 * screen is right until a thread is older than the window and then quietly
 * wrong, in the direction nobody checks: a reader who has been shown a number
 * stops asking. So the fixture deliberately pushes the start of the thread out
 * of the window and asserts the count is still the whole thread's.
 *
 * A room of its own, because a count is about a fixture and a shared room is
 * whatever the suite did to it before this ran.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/thread-count-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// The console's own window. If this and CHAT_WINDOW in ChatRoom.tsx drift, the
// check stops measuring the thing it is named for - the filler below would no
// longer push the root off the page - so it says so rather than passing.
const WINDOW = 60;

const room = "threadcount";
const say = async (body, thread) => {
  const res = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(thread ? { body, thread } : { body }),
  });
  if (!res.ok) die(`could not seed the room: ${res.status} ${await res.text()}`);
  return res.json();
};

// A thread whose beginning will be out of the window, and a lone message that
// must NOT be given a count - the two answers have to be told apart, and a
// check with only the first would pass on a console that drew a chip on
// everything.
const stamp = `${Date.now().toString(36)}`;
const root = await say(`root of the counted thread ${stamp}`);
await say(`first reply ${stamp}`, root.thread);
await say(`second reply ${stamp}`, root.thread);

// Enough to push the three above off the window. Sequential rather than
// parallel: the node stamps an ordering and a burst of concurrent writes is a
// fixture whose order depends on the scheduler.
for (let i = 0; i < WINDOW; i++) await say(`filler ${i} ${stamp}`);

const alone = await say(`nobody answered this one ${stamp}`);
const last = await say(`third reply, long after the root ${stamp}`, root.thread);

// WHAT THE NODE SAYS THE COUNT IS, asked before the browser opens. The check
// compares the screen against this rather than against a number typed in here,
// so a fixture that seeded differently than intended fails as a mismatch
// instead of passing against its own assumption.
const page1 = await fetch(`${base}/api/chat/${room}?thread=${encodeURIComponent(root.thread)}`, {
  headers: { Authorization: `Bearer ${token}` },
});
if (!page1.ok) die(`could not read the thread back: ${page1.status}`);
const inThread = ((await page1.json()).events || []).length;
if (inThread !== 4) die(`the fixture has ${inThread} messages in the thread, wanted 4`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 });

  // The last message in the room is the one to hang the assertions on: it is in
  // view by construction, and its thread starts sixty-odd messages back.
  const lastRow = page.locator(`[data-message="${last.id}"]`);
  await lastRow.waitFor({ state: "visible", timeout: 20_000 });

  const chip = lastRow.locator(`[data-thread-replies="${root.thread}"]`);
  try {
    await chip.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("a message in a four-message thread offers no way to see the thread it is in");
  }
  const counted = Number(await chip.getAttribute("data-thread-count"));
  if (counted !== inThread) {
    die(
      `the transcript counted ${counted} in the thread and the node holds ${inThread} - the count is being folded from the window, so it is wrong for every thread older than one`,
    );
  }
  const label = (await chip.innerText()).trim();
  if (label !== `${inThread - 1} replies`) {
    die(`the chip reads ${JSON.stringify(label)}, want "${inThread - 1} replies"`);
  }

  // AND A MESSAGE NOBODY ANSWERED IS NOT GIVEN A NUMBER. A thread always holds
  // at least the message drawing it, so a console that drew the raw count would
  // put "1" or "0 replies" on every line in the room - a number that says
  // nothing, on every message, which is how a reader learns to stop reading it.
  const aloneRow = page.locator(`[data-message="${alone.id}"]`);
  await aloneRow.waitFor({ state: "visible", timeout: 10_000 });
  if ((await aloneRow.locator("[data-thread-replies]").count()) !== 0) {
    die("a message nobody answered is drawn as having replies");
  }

  // THE WAY IN. Pressing the thread id opens the pane on THAT thread, which is
  // the half the operator actually asked for - "impossible to track things
  // here" was about reading a conversation, not about counting one.
  await lastRow.locator(`[data-thread-open="${last.id}"]`).click();
  const pane = page.locator('[data-room-pane-body="thread"]');
  await pane.waitFor({ state: "visible", timeout: 10_000 });
  // The ROOT's words, in the pane, from a message sixty lines further down the
  // room: proof the pane holds the thread and not the neighbourhood of the
  // message that was clicked.
  try {
    await pane
      .getByText(`root of the counted thread ${stamp}`, { exact: false })
      .first()
      .waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("the thread pane opened without the thread's first message in it");
  }
  if (!page.url().includes(`/thread/${last.id}`)) {
    die(`opening a thread left the url at ${page.url()}, so it cannot be sent to anybody`);
  }

  console.log(
    `thread ${root.thread.slice(-6)}: ${inThread - 1} replies on the chip with the root ${WINDOW + 1} messages out of the window, no chip on an unanswered message, and the id opened the pane on the whole thread`,
  );
} finally {
  await browser.close();
}
