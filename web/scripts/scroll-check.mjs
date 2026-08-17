/**
 * Reading the history wins over following the room.
 *
 * Scroll back, let a message arrive, and the view must NOT move - with a way
 * back down offered instead. Reported by the user, who was reading the
 * transcript and kept being dragged to the end whenever anybody spoke.
 *
 *   node scripts/scroll-check.mjs BASE_URL TOKEN [POST_TOKEN] [ROOM]
 *
 * The assertion is THE SCROLL POSITION, not the pill. A version that renders
 * the pill and still jumps would pass any check that only asked whether the
 * pill was there - and jumping is the entire complaint. The pill is checked
 * second, because a view that stays put and never says anything arrived is a
 * different bug rather than a fix.
 *
 * POST_TOKEN is a second principal IN THE SAME PROJECT AS THE READER. Somebody
 * else has to say it, because a message the reader sent arrives by a different
 * path - but rooms are per project, so a principal from another project says it
 * into their own `general` and this waits out the timeout on a room that never
 * heard it. That looked like a broken pill, then like a broken poll, and was
 * neither.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, postToken, roomArg] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/scroll-check.mjs BASE_URL TOKEN [POST_TOKEN] [ROOM]");
  process.exit(2);
}

// This check seeds fixtures, so it must not be aimed at a live node by accident.
refuseRemote(base, "scroll-check");

// ITS OWN ROOM BY DEFAULT, because this check WRITES - it seeds padding to have
// something to scroll back through. Against the gate's throwaway node that is
// harmless; pointed at a live node it dumps fixtures into whatever room you
// named, and #general is the room the operator reads. That happened: fifty
// padding messages in a person's transcript, from running a check by hand
// against production. Rooms are a column here, so a fixture room costs nothing
// and a shared one costs somebody their reading.
const room = roomArg || "scrollcheck";

// The app calls a reader "at the bottom" when they are within 24px of it, and
// auto-scrolls those readers on purpose - following the room is right when you
// are already at the end. So this check is only meaningful with enough history
// to put the reader UNAMBIGUOUSLY above that line. It used to just ask for
// 1500px of scrollback and take whatever it got: in a freshly seeded room that
// was ~252px, `scrollTop` clamped to 0, and the reader sat a couple of hundred
// pixels from the bottom - close enough that the outcome depended on the height
// of the arriving row. The check then failed a correct implementation and
// passed it on the next run, which is worse than not having it: one flaky check
// in a 543-check gate makes every red ambiguous.
const NEED_SCROLLBACK = 900;

/** seedHistory posts until the transcript is tall enough to read back into. */
async function seedHistory(base, poster, howMany) {
  for (let i = 0; i < howMany; i++) {
    const r = await fetch(`${base}/api/chat/${room}/say`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${poster}` },
      body: JSON.stringify({
        body: `scroll-check history ${i} - padding so a reader has somewhere to scroll back to`,
      }),
    }).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
    // Checked, not fired and forgotten: a refused post that looks like a
    // successful one is how this file's other bug spent twenty seconds blaming
    // the view for a message the node never took.
    if (!r.ok) {
      console.error(`could not seed history: POST say -> ${r.status} ${await r.text()}`);
      process.exit(1);
    }
  }
}

const browser = await chromium.launch();
try {
  await seedHistory(base, postToken || token, 25);
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});
  await page.waitForSelector("main div.whitespace-pre-wrap", { timeout: 15_000 }).catch(() => {});

  // The scroller is the element that actually overflows, found by asking the
  // page rather than by guessing a selector that a class rename would break.
  const scroller = await page.evaluateHandle(() => {
    const el = document.querySelector("main div.whitespace-pre-wrap");
    let p = el?.parentElement;
    while (p && p.scrollHeight <= p.clientHeight) p = p.parentElement;
    return p;
  });
  const scrollable = await scroller.evaluate((el) => !!el && el.scrollHeight > el.clientHeight);
  if (!scrollable) {
    console.error("the transcript does not scroll here, so nothing about scrolling was tested");
    process.exit(1);
  }

  // WAIT FOR THE ROOM TO STOP GROWING before taking a position to compare
  // against. The seeded messages arrive by the same poll as everything else, so
  // loading the page immediately after seeding means the transcript is still
  // filling in while the reader is placed - the content grows underneath them
  // and the position drifts for a reason that has nothing to do with the
  // behaviour under test. Measured: 375 -> 409 in a freshly seeded room, a
  // "jump" that was just the last of the padding landing.
  let settled = -1;
  for (let waited = 0; waited < 15_000; waited += 400) {
    const h = await scroller.evaluate((el) => el.scrollHeight);
    if (h === settled) break;
    settled = h;
    await page.waitForTimeout(400);
  }

  // Scroll well back into the history, the way somebody reading does.
  await scroller.evaluate((el) => {
    el.scrollTop = Math.max(0, el.scrollHeight - el.clientHeight - 1500);
  });
  await page.waitForTimeout(300);
  const before = await scroller.evaluate((el) => el.scrollTop);

  // The reader must be FAR ENOUGH from the bottom that following-the-room and
  // holding-position are distinguishable outcomes. Below this the app is
  // entitled to scroll and a "jump" proves nothing, so say the check could not
  // run rather than reporting a failure it cannot support.
  const fromBottom = await scroller.evaluate(
    (el) => el.scrollHeight - el.scrollTop - el.clientHeight,
  );
  if (fromBottom < NEED_SCROLLBACK) {
    console.error(
      `not enough history to test: the reader is only ${Math.round(fromBottom)}px from the bottom, and the app treats anything under 24px as "at the end".
  A reader this close cannot tell "held position" apart from "followed the room", so a jump here would not be evidence of the bug.
  Seed more messages into #${room} before this check.`,
    );
    process.exit(1);
  }

  // Something arrives, from somebody else.
  const rows = () => page.locator("main div.whitespace-pre-wrap").count();
  const rowsBefore = await rows();
  // The post is CHECKED. It used to be fetch(...).catch(() => {}), so a
  // refused or failed post looked exactly like a successful one, and the check
  // then spent twenty seconds waiting for a message that was never accepted
  // and blamed the view for not showing it. A refusal nobody sees is
  // indistinguishable from success - including when it is your own harness
  // doing the not-seeing.
  const poster = postToken || token;
  const said = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${poster}` },
    body: JSON.stringify({ body: `scroll-check probe ${Date.now()}` }),
  }).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
  if (!said.ok) {
    console.error(
      `the probe was not accepted: HTTP ${said.status} ${await said.text()}
  Nothing about scrolling was tested. This is the poster's credentials or the
  room, not the view.`,
    );
    process.exit(1);
  }

  // WAIT FOR IT TO ARRIVE, rather than guessing how long a poll takes. This
  // was a flat 4s and the room's long poll did not always answer inside it, so
  // the check failed saying the pill was missing when the message had simply
  // not landed yet - blaming the code under test for the harness being in a
  // hurry. A deadline that waits for the event it is about is not slower in
  // the normal case: it returns as soon as the row appears.
  let rowsAfter = rowsBefore;
  for (let waited = 0; waited < 20_000 && rowsAfter <= rowsBefore; waited += 250) {
    await page.waitForTimeout(250);
    rowsAfter = await rows();
  }
  if (rowsAfter <= rowsBefore) {
    // Say WHERE it stopped. "The view never received it" is three different
    // faults wearing one sentence - the room never got it, the reader cannot
    // see it, or the page is not polling - and the next person should not have
    // to run this again with print statements to find out which.
    const seen = await fetch(`${base}/api/chat/${room}?limit=500`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((r) => r.json())
      .then((j) => (j.events || []).length)
      .catch((err) => `unreadable: ${err}`);
    const banner = await page
      .locator("main .text-destructive, .text-destructive")
      .first()
      .textContent()
      .catch(() => null);
    console.error(
      `nothing arrived: ${rowsBefore} rows before, ${rowsAfter} after 20s.
  the room's API returns ${seen} events to the READER's own token
  the page's error banner says: ${banner ? banner.trim() : "(nothing)"}
  If the API count grew and the rows did not, the poll or the render is at
  fault, not the room. If it did not grow, the post went somewhere else.`,
    );
    process.exit(1);
  }

  // Now let any scroll it provoked finish, so the position below is where the
  // view SETTLED rather than where it was passing through.
  await page.waitForTimeout(1500);
  const after = await scroller.evaluate((el) => el.scrollTop);

  if (Math.abs(after - before) > 24) {
    console.error(
      `the view jumped while reading: scrollTop ${before} -> ${after}.
  A message arriving must not move a reader who has scrolled back.`,
    );
    process.exit(1);
  }

  const pill = await page.locator("main button", { hasText: "jump to latest" }).count();
  if (pill === 0) {
    console.error(
      "the view stayed put but nothing said a message had arrived - " +
        "silence about new messages is a different bug, not a fix",
    );
    process.exit(1);
  }

  console.log(`ok  reading wins: scrollTop held at ${after}, and the new-message pill is offered`);
} finally {
  await browser.close();
}
