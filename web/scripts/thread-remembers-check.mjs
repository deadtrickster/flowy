/**
 * The thread pane puts back the thread you left it on.
 *
 *   node scripts/thread-remembers-check.mjs BASE_URL TOKEN OTHER_TOKEN
 *
 * THE OPERATOR, 01M0JPYYDZ: "visiting other panel and returned to room resets
 * the thread panel. thread panel should restore to the thread I was at when
 * leaving room panel".
 *
 * Switching PANES already worked - a pane is a route and ChatRoom stays mounted,
 * so its `opened` state survives. Leaving the ROOM unmounts the component,
 * `opened` goes with it, and the path the sidebar returns you to names no
 * message. So the pane came back empty and the thread had to be found again.
 *
 * TWO ROOMS, because the fix is per-room and the failure it must not introduce
 * is the opposite one: a thread remembered globally would follow the reader into
 * a room where no event matches it, and draw an empty pane that reads as broken.
 * That bug exists on master today for a different reason - nothing resets
 * `opened` when the room param changes - so the second arm is a regression test
 * for a defect this change also fixes.
 *
 * THE NEGATIVE ARM IS THE LOAD-BEARING ONE. "It remembers" is satisfiable by a
 * pane that shows the same thread everywhere, which is worse than forgetting.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, other] = process.argv.slice(2);
if (!base || !token || !other) {
  console.error("usage: node scripts/thread-remembers-check.mjs BASE_URL TOKEN OTHER_TOKEN");
  process.exit(2);
}

refuseRemote(base, "thread-remembers-check");

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const say = async (where, body, as) => {
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(where)}/say`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${as}`,
    },
    body: JSON.stringify(body),
  });
  if (!r.ok) die(`saying in #${where} answered ${r.status} ${await r.text()}`);
  return r.json();
};

const stamp = String(process.hrtime.bigint()).slice(-8);
/**
 * ITS OWN TWO ROOMS, not #general.
 *
 * @orchestrator swept the console checks tonight and found five reading the
 * shared room - the one every other check raises rows into. priority-check had
 * already flipped on it: PASS alone, FAIL in a full suite, because the panel it
 * opens holds three rows in isolation and everybody else's in a suite. A check
 * over a shared surface measures the suite's history, and which way it lands
 * depends on what ran before it.
 *
 * A room exists here as soon as somebody speaks in it - rooms-scroll-check makes
 * its own for the same reason. So these two hold exactly what this check put
 * there, and no other check's rows can move what it asserts.
 */
const room = `threadmem-a-${stamp}`;
const otherRoom = `threadmem-b-${stamp}`;
// A thread with a reply, so the pane has something to be right about, and a
// distinctive word so the assertion is about THIS thread rather than any.
const word = `remembers-${stamp}`;
const root = await say(room, { body: `thread-remembers-check root ${word}` }, other);
await say(room, { body: `thread-remembers-check reply ${word}`, thread: root.thread }, other);
// And a message in the other room, so it is a room with content rather than an
// empty one - an empty pane there would be ambiguous.
await say(otherRoom, { body: `thread-remembers-check elsewhere ${stamp}` }, other);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    viewport: { width: 1500, height: 950 },
  });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const pane = () => page.locator('[data-room-pane-body="thread"]');

  // OPEN IT the way a reader does, rather than by navigating to the deep link -
  // the deep-linked path names the message and would restore for a reason this
  // check is not about.
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});
  const opener = page.locator(`text=${word}`).first();
  try {
    await opener.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(`the seeded message ${word} never reached #${room}`, crashes.join("\n"));
  }
  const threadControl = page.locator("[data-thread-open]").first();
  if ((await threadControl.count()) === 0) {
    die("no control carrying data-thread-open, so a reader cannot open a thread at all");
  }
  await threadControl.click();
  try {
    await pane().locator(`text=${word}`).first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(
      "opening a thread did not put it in the thread pane",
      await pane()
        .innerText()
        .catch(() => ""),
    );
  }

  // LEAVE THE ROOM ALTOGETHER, which is the reported gesture - not a pane switch.
  await page
    .goto(`${base}/chat/${encodeURIComponent(otherRoom)}`, { timeout: 30_000 })
    .catch(() => {});
  await page.waitForTimeout(500);

  // THE NEGATIVE ARM FIRST, because it is the one a naive fix breaks: the other
  // room must NOT be showing the thread from the first.
  const elsewhere = await pane()
    .innerText()
    .catch(() => "");
  if (elsewhere.includes(word)) {
    die(
      `#${otherRoom}'s thread pane is showing #${room}'s thread - a thread remembered globally\nfollows the reader into rooms where nothing matches it`,
      elsewhere,
    );
  }

  // AND BACK TO THE THREAD PANE, which is the reported gesture: the reader was
  // ON the thread pane when they left. Returning to /chat/<room> with no pane
  // segment lands on whatever pane is default, so the thread pane would not be
  // drawn at all - that is a different question, and one for the operator.
  await page
    .goto(`${base}/chat/${encodeURIComponent(room)}/thread`, { timeout: 30_000 })
    .catch(() => {});
  try {
    await pane().locator(`text=${word}`).first().waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(
      `coming back to #${room} lost the thread that was open - the pane shows something else`,
      await pane()
        .innerText()
        .catch(() => ""),
    );
  }

  console.log("ok: the thread pane puts back the thread you left it on, per room");
} finally {
  await browser.close();
}
