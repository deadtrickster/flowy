/**
 * The unread badge clears, in a real browser, and the node's mark moves with it.
 *
 *   node scripts/unread-check.mjs BASE_URL READER_TOKEN OTHER_TOKEN
 *
 * Reported as "unread counter is stuck": the sidebar badge never cleared for
 * the operator. The count came from the inbox - what this principal may read
 * and did not write, since the reader mark the node keeps for it - and that
 * mark is moved by a WAITER acking. `inbox_readers` held rows for every agent
 * on the node and no row at all for the person in the browser, so nothing ever
 * moved theirs and the number only grew.
 *
 * BOTH HALVES ARE ASSERTED, and that is the point of this file. A badge that
 * clears while the mark stays put is the same bug in a new costume - the next
 * device, or the next reload, reads the log from the same place and paints the
 * same number. So every clearing assertion below is paired with a read of
 * /api/inbox/readers, which is where the node says that mark actually is.
 *
 * OTHER_TOKEN is a second principal IN THE SAME PROJECT AS THE READER, for the
 * reason scroll-check.mjs learned it: rooms are per project, so a say on a
 * token from another project lands in that project's `general` and this waits
 * out its deadline on a room that never heard it. It also has to be somebody
 * else - the inbox excludes what you wrote yourself, which is a rule this
 * check relies on twice, once on purpose at the end.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, otherToken] = process.argv.slice(2);
if (!base || !token || !otherToken) {
  console.error("usage: node scripts/unread-check.mjs BASE_URL READER_TOKEN OTHER_TOKEN");
  process.exit(2);
}

// This check seeds fixtures, so it must not be aimed at a live node by accident.
refuseRemote(base, "unread-check");

const ROOM = "general";
const READER = `console:${ROOM}`;

const fail = (message) => {
  console.error(message);
  process.exit(1);
};

const call = async (path, init = {}, as = token) => {
  const response = await fetch(`${base}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${as}`, ...init.headers },
  });
  const text = await response.text();
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = text;
  }
  return { ok: response.ok, status: response.status, body };
};

/** say posts into the room and CHECKS it was accepted - a refusal nobody sees
 * looks exactly like a message the view failed to draw, and this check would
 * then blame the badge for a token problem. */
const say = async (as, body) => {
  const said = await call(
    `/api/chat/${ROOM}/say`,
    { method: "POST", body: JSON.stringify({ body }) },
    as,
  );
  if (!said.ok) {
    fail(`the probe was not accepted: HTTP ${said.status} ${JSON.stringify(said.body)}
  Nothing about unread was tested. This is the poster's credentials or the room.`);
  }
  return said.body;
};

/** mark is where the node says the console's reader for this room stands, or
 * null when there is no such reader - which is the state the bug lives in. */
const mark = async () => {
  const held = await call("/api/inbox/readers");
  if (!held.ok) fail(`GET /api/inbox/readers answered HTTP ${held.status}`);
  const row = (held.body.readers ?? []).find((r) => r.reader === READER);
  return row ? row.cursor : null;
};

/**
 * until polls for something to become true and answers whether it did. The
 * badge is cleared by an acknowledgement that crosses the wire, so every
 * assertion about it is "within a deadline" rather than "on the next line".
 */
const until = async (page, ready, ms = 20_000) => {
  for (let waited = 0; waited < ms; waited += 250) {
    if (await ready()) return true;
    await page.waitForTimeout(250);
  }
  return ready();
};

// A clean slate for THIS assertion, not for the node: the other console checks
// mount the same console under the same token, so the reader may already be
// there from one of them. Deleting it is how the first assertion below can be
// about a principal that has never read from a browser.
//
// CHECKED, because a delete that quietly did not happen leaves a reader that is
// already past everything - and then the first assertion below passes without
// testing anything, which is the failure mode this whole file is written
// against in the code it is testing.
await call(`/api/inbox/reader/${READER}`, { method: "DELETE" });
if ((await mark()) !== null) {
  fail(`${READER} is still there after being deleted, so this run would start
  from a reader that has already read everything and would assert nothing.`);
}

// Said before the console has ever been opened. Under the rule this check is
// about, they are history rather than unread: a first load must not report the
// log at somebody.
//
// The readings come off the messages themselves rather than off a page of the
// room, because a room read answers the OLDEST page of the log and this needs
// the newest reading, exactly.
let backlog = 0;
for (let i = 0; i < 4; i++) backlog = (await say(otherToken, `unread-check backlog ${i}`)).seq_hlc;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const badge = page.locator(`[data-unread="${ROOM}"]`);
  const inboxRead = () =>
    page.waitForResponse(
      (r) => r.url().includes("/api/inbox/unread?") && r.url().includes(`room=${ROOM}`),
      { timeout: 40_000 },
    );

  // ---- a principal with no reader row starts at the head, not at the log ----

  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});

  // Wait for the console to HAVE declared its reader before reading the badge.
  // Without this witness, "no badge" would also be the answer from a page that
  // never loaded, which is the way this check could pass while saying nothing.
  let at = null;
  for (let waited = 0; waited < 20_000 && at === null; waited += 250) {
    await page.waitForTimeout(250);
    at = await mark();
  }
  if (at === null) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    fail(`the console declared no reader called ${READER}.
  Nothing moves a mark for a person in a browser - no waiter is running for
  them - so the inbox never shrinks and the badge only grows. That is the bug.${errors}`);
  }
  if (at < backlog) {
    fail(`the console's reader was declared at ${at}, behind the room's newest message ${backlog}.
  A first load would report everything ever said as unread.`);
  }
  if ((await badge.count()) > 0) {
    fail(`a first load shows ${await badge.first().textContent()} unread in #${ROOM}.
  Nobody has ever read this room from a console under this token, so there is
  nothing to have missed: the head is where a new reader starts, exactly as
  \`flowy inbox --new\` starts a waiter.`);
  }

  // ---- what arrives afterwards is counted, on the badge's own clock ----

  let reached = 0;
  for (let i = 0; i < 3; i++) {
    reached = (await say(otherToken, `unread-check arrival ${i}`)).seq_hlc;
  }
  // On the badge's own clock, which is a timer: the console is sitting on the
  // overview and nothing about this page is watching the room.
  if (!(await until(page, async () => (await badge.count()) > 0, 40_000))) {
    fail(`three messages were said in #${ROOM} and no badge appeared.
  A badge that never counts is as useless as one that never clears.`);
  }
  const counted = (await badge.first().textContent())?.trim();
  if (counted !== "3") {
    fail(`the badge says ${counted} after three messages arrived in #${ROOM}.`);
  }

  // ---- reading the room clears the badge AND moves the mark ----
  //
  // The mark first, because it is the half that cannot be faked by the tab:
  // waiting for the badge to go and calling that a pass is exactly what a
  // per-tab latch would satisfy.

  const before = await mark();
  await page.goto(`${base}/chat/${ROOM}`, { timeout: 20_000 }).catch(() => {});
  await page.waitForSelector("main [data-body]", { timeout: 15_000 }).catch(() => {});
  // READ IT THE WAY A PERSON DOES: to the end of the transcript. What clears
  // the badge is reaching the bottom, not opening the room, so the check has to
  // do the reaching - and it has to keep doing it while the room fills, because
  // a message that lands while the view is still scrolling leaves the reader
  // legitimately short of the end, with the pill offering the way down. Waiting
  // for the view to land there on its own is what made a first version of this
  // check pass one run and fail the next, blaming the badge for the scroll rule
  // it was racing.
  const toBottom = () =>
    page.evaluate(() => {
      const body = document.querySelector("main [data-body]");
      let scroller = body?.parentElement;
      while (scroller && scroller.scrollHeight <= scroller.clientHeight) {
        scroller = scroller.parentElement;
      }
      if (scroller) scroller.scrollTop = scroller.scrollHeight;
    });
  if (
    !(await until(
      page,
      async () => {
        await toBottom();
        return (await mark()) >= reached;
      },
      30_000,
    ))
  ) {
    fail(`the room was read to the end and the node's mark did not move: ${READER} was at
  ${before}, is at ${await mark()}, and the newest message in the room is ${reached}.
  The badge is drawn from that mark on every device, so a mark that stays put is
  a room that is bold again on the next reload and on the next browser.`);
  }
  const after = await mark();
  if (!(await until(page, async () => (await badge.count()) === 0))) {
    fail(`the mark moved to ${after} and #${ROOM} still shows ${await badge
      .first()
      .textContent()} unread. The badge and the mark disagree, which is the
  stuck counter with the two halves swapped.`);
  }

  // ---- a stale ack must not drag the mark backwards ----
  //
  // Two tabs, or a slow one behind a fast one, hand back different positions.
  // The node keeps the mark moving forwards only; this is that rule exercised
  // through the label the console actually uses.
  //
  // A THOUSAND back, not one. `after - 1 === after` at this magnitude: a
  // reading is a 57-bit number and this is javascript, which is the whole
  // reason the console acks a message id rather than a reading, and it would
  // have made this assertion pass against a node that had no such rule at all.
  const stale = await call("/api/inbox/ack", {
    method: "POST",
    body: JSON.stringify({ as: READER, cursor: Math.max(0, after - 1000), delivered: true }),
  });
  if (!stale.ok) fail(`POST /api/inbox/ack answered HTTP ${stale.status}`);
  if (stale.body.cursor !== after) {
    fail(`an ack of an older position moved the mark from ${after} to ${stale.body.cursor}.
  Two tabs would then take turns reopening messages the other had read.`);
  }

  // ---- your own messages are not news to you ----
  //
  // The inbox has always excluded what you wrote - store.EventQuery.NotActors -
  // and the badge is built on the inbox precisely so that it inherits that
  // rather than reimplementing it. Asserted here because a badge that counts
  // your own messages cannot be cleared by reading them: nothing new arrives to
  // move the mark past your own words.
  // Off the room first, so nothing here is acking while it runs.
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  for (let i = 0; i < 2; i++) await say(token, `unread-check said by the reader ${i}`);
  // A fresh mount reads the marks and the inbox again, which is a full round
  // trip through the node rather than a wait on a timer. The wait is armed
  // before the load, because the load is what makes the request.
  const refilled = inboxRead();
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await refilled;
  await page.waitForTimeout(500);
  if ((await badge.count()) > 0) {
    fail(`#${ROOM} shows ${await badge.first().textContent()} unread after the reader
  said two things in it themselves. Your own messages are not news to you.`);
  }

  if (crashes.length > 0) fail(`the console threw while doing it:\n  ${crashes.join("\n  ")}`);

  console.log(
    `ok  a first load counted nothing of the log up to ${backlog}, three arrivals counted 3, reading #${ROOM} cleared them and moved ${READER} to ${after}, an older ack moved nothing, and the reader's own two were not counted`,
  );
} finally {
  await browser.close();
}
