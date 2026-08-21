/**
 * The thread pane shows the thread the reader opened, and the room does not
 * move it.
 *
 *   node scripts/pane-stays-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM
 *
 * THE OPERATOR: "that thread panel just changed while I was typing - it changes
 * to the latest unthreaded message", then "it should keep showing thread I
 * opened".
 *
 * The cause was one expression: `selected?.thread ?? events.at(-1)?.thread`.
 * With nothing selected the pane followed the last event in the room, so every
 * message anybody said re-pointed it - and with four agents in a room that is
 * every few seconds.
 *
 * SO THE ASSERTION IS ABOUT A MESSAGE THE READER DID NOT SEND. A second
 * principal says something while the pane is open, and the pane must not care.
 * That is why this takes two tokens: a reader cannot provoke the bug alone,
 * because the bug is the ROOM moving the pane, not the reader.
 */

import { chromium } from "playwright";

const [base, token, other, room] = process.argv.slice(2);
if (!base || !token || !other || !room) {
  console.error("usage: node scripts/pane-stays-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const say = async (bearer, body) => {
  // /say, not the room path. GET on the room reads it and POST on it is not a
  // door at all - the 404 that answers looks exactly like a room that is not
  // there, and this check spent a gate pass saying so.
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
    method: "POST",
    headers: { Authorization: `Bearer ${bearer}`, "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!r.ok) die(`saying in #${room} answered ${r.status} ${await r.text()}`);
  return r.json();
};

// TWO THREADS OF ITS OWN, seeded here rather than found in the room.
//
// This needs at least two threads to open - "the pane held what I opened" and
// "the pane followed the room" look identical when there is only one. It used
// to say one message and rely on whatever else the suite had left in #general,
// which is a check that passes because of its neighbours: measured under ONLY=,
// where the room holds one message and this died reporting "only 1 thread
// buttons" about a feature that was fine.
await say(other, "pane-stays-check: a message to open a thread on");
await say(other, "pane-stays-check: a second thread, so there are two to tell apart");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});
  await page
    .locator("[data-thread-open]")
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // NOTHING IS OPEN BEFORE ANYTHING IS OPENED. The empty pane is the point: a
  // pane showing a conversation nobody chose is the defect.
  const paneThread = () =>
    page
      .locator("[data-thread-pane-id]")
      .first()
      .getAttribute("data-thread-pane-id")
      .catch(() => null);

  const opens = page.locator("[data-thread-open]");
  const count = await opens.count();
  if (count < 2)
    die(
      `only ${count} thread buttons in #${room} - not enough to tell "the one I opened" from "the newest"`,
    );

  // Open the thread of an OLD message - deliberately not the newest, so that
  // "the pane held" and "the pane followed the room" cannot look the same.
  const target = opens.nth(0);
  const wanted = await target.getAttribute("data-thread-open");
  await target.click();
  await page.waitForTimeout(800);
  const after = await page.locator("[data-thread-pane-id]").count();
  if (after === 0) {
    die(`clicking a thread button opened no pane. Looked for [data-thread-pane-id];
if the pane renders without that attribute this check cannot see which thread it
is on, which is the whole question.`);
  }
  const held = await paneThread();

  // NOW THE ROOM MOVES UNDER IT. The other seat says something new; the pane
  // must be on the same thread afterwards.
  await say(other, "pane-stays-check: a newer message the reader did not open");
  await page.waitForTimeout(2500);
  const still = await paneThread();
  if (still !== held) {
    die(`the pane moved when somebody else spoke: it was on ${held} and is now on ${still}.
The reader opened ${wanted} and did not touch it. That is the operator's report -
"it changes while I was typing" - and it is what this check exists for.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`the pane held ${held} across a message from another seat`);
} finally {
  await browser.close();
}
