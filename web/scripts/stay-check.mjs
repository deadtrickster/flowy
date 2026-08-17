/**
 * A ROOM LOAD LANDS AT THE END AND STAYS THERE.
 *
 *   node scripts/stay-check.mjs BASE_URL TOKEN [POST_TOKEN] [ROOM]
 *
 * Reported as "when I reload a page chat automatically scrolls to a random
 * place". It was not random and it was not the load: the view arrived at the
 * end correctly and was displaced SECONDS LATER, and what a reader sees in
 * that window is an arbitrary point in the transcript.
 *
 * The cause was a race between smooth scrolls. A room's history does not
 * arrive in one answer - the first GET carries a page of 200 and the long poll
 * delivers the rest - so the transcript scrolled itself once per batch, and a
 * smooth scroll latches its destination when it is CALLED. Several animations
 * ran at once, each aimed at the bottom of a shorter transcript, and whichever
 * the browser finished last is where the reader was left. Measured over eight
 * loads of a 718 message room: four landed 201,633px short, at the end of the
 * room as it stood at 399 messages.
 *
 * SO THIS CHECK WATCHES, IT DOES NOT SAMPLE. An assertion taken once when the
 * page looks settled passes this bug outright - the displacement happened
 * about five seconds after the view first looked correct, and the losing
 * animation had already fired scrollend, so nothing retried and nothing said
 * anything was wrong. The check therefore waits for the transcript to stop
 * growing, asserts the reader is at the end, and then keeps asking for
 * WATCH_MS while nothing whatsoever arrives.
 *
 * It needs MORE THAN ONE PAGE of history, because a room that arrives in a
 * single answer scrolls once and has no race to lose. The seed is sized from
 * the node's own page limit rather than a number copied from it.
 *
 * The pill is asserted too, and it is not a nicety here: "N new messages" over
 * a room nobody has touched means the view counted history it was still
 * drawing as unread, which is the same stale reading seen from the other side.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, postToken, roomArg] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/stay-check.mjs BASE_URL TOKEN [POST_TOKEN] [ROOM]");
  process.exit(2);
}

// This check seeds fixtures, so it must not be aimed at a live node by accident.
refuseRemote(base, "stay-check");

// Its own room, for the reason scroll-check has one: this WRITES, and a shared
// room costs somebody their reading. See localonly.mjs.
const room = roomArg || "staycheck";

/**
 * A page of chat is 200 rows (store.defaultLimit), so the seed has to be
 * comfortably over that or the whole room lands in the first answer and the
 * batches this is about never happen. Two and a bit pages is enough to have
 * three different stale targets to lose to, and cheap enough to seed.
 */
const PAGE = 200;
const SEED = PAGE * 2 + 50;

/** How long the reader must stay put after the transcript has settled. */
const WATCH_MS = 12_000;

/** How far off the end still counts as the end - the app's own tolerance. */
const AT_END = 24;

/**
 * seedHistory posts the padding, a few at a time. Sequentially it is a minute
 * of the gate's wall clock for no gain: these are padding, and the only thing
 * that matters about them is that there are more than a page.
 */
async function seedHistory(poster, howMany) {
  const lanes = 10;
  let next = 0;
  const post = async () => {
    while (next < howMany) {
      const i = next++;
      const r = await fetch(`${base}/api/chat/${room}/say`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${poster}` },
        body: JSON.stringify({
          body: `stay-check history ${i} - padding, so the room needs more than one page to arrive`,
        }),
      }).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
      // Checked, not fired and forgotten: a refused post that looks like a
      // successful one is how a sibling check spent twenty seconds blaming the
      // view for a message the node never took.
      if (!r.ok) {
        console.error(`could not seed history: POST say -> ${r.status} ${await r.text()}`);
        process.exit(1);
      }
    }
  };
  await Promise.all(Array.from({ length: lanes }, post));
}

/** How many messages the room already holds, so a re-run does not re-seed it. */
async function held() {
  const r = await fetch(`${base}/api/chat/${room}?limit=1000`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!r.ok) {
    console.error(`could not read #${room}: HTTP ${r.status} ${await r.text()}`);
    process.exit(1);
  }
  return ((await r.json()).events || []).length;
}

const already = await held();
if (already < SEED) await seedHistory(postToken || token, SEED - already);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});
  await page.waitForSelector("main div.whitespace-pre-wrap", { timeout: 15_000 }).catch(() => {});

  // The scroller is the element that actually overflows, found by asking the
  // page rather than by guessing a selector a class rename would break.
  const scroller = await page.evaluateHandle(() => {
    const el = document.querySelector("main div.whitespace-pre-wrap");
    let p = el?.parentElement;
    while (p && p.scrollHeight <= p.clientHeight) p = p.parentElement;
    return p;
  });
  const where = () =>
    scroller.evaluate((el) =>
      el
        ? {
            top: Math.round(el.scrollTop),
            fromEnd: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
            height: el.scrollHeight,
          }
        : null,
    );
  const rows = () => page.locator("main div.whitespace-pre-wrap").count();

  if (!(await where())) {
    console.error("the transcript does not scroll here, so nothing about scrolling was tested");
    process.exit(1);
  }

  // SETTLED means the room has stopped arriving: the row count and the height
  // both stop changing. Waiting a flat number of seconds instead would either
  // start the watch while the last batch was still landing - and blame the view
  // for content growing underneath it - or spend the difference doing nothing.
  let settled = null;
  let stableFor = 0;
  for (let waited = 0; waited < 25_000; waited += 400) {
    const now = await where();
    const n = await rows();
    const key = `${n}:${now.height}`;
    stableFor = key === settled ? stableFor + 400 : 0;
    settled = key;
    if (stableFor >= 1200) break;
    await page.waitForTimeout(400);
  }
  const rowsSettled = await rows();
  if (rowsSettled < SEED) {
    console.error(
      `the room never finished arriving: ${rowsSettled} rows on screen, ${SEED} seeded.
  Nothing about scrolling was tested - this is the poll or the seed, not the view.`,
    );
    process.exit(1);
  }
  if (rowsSettled <= PAGE) {
    // The whole point is a room that arrives in SEVERAL answers. One that fits
    // in a page scrolls once, has no losing animation, and would pass this
    // check under the bug it exists for.
    console.error(
      `#${room} arrived in one page (${rowsSettled} rows, page is ${PAGE}), so the batched
  arrival this check is about never happened. Seed more than a page.`,
    );
    process.exit(1);
  }

  const arrived = await where();
  if (arrived.fromEnd > AT_END) {
    console.error(
      `the room load did not land at the end: ${arrived.fromEnd}px short (scrollTop ${arrived.top} of ${arrived.height}).
  Opening a room puts the reader at the newest message. Landing short by more than a screen
  means a scroll aimed at the end of a transcript that has since grown - see MessageList.`,
    );
    process.exit(1);
  }

  // AND NOW THE PART THAT CATCHES THE REPORTED BUG. Nothing is posted, nothing
  // is clicked, nobody scrolls. The reader must still be here in twelve
  // seconds' time. Under the bug this window is where the view was thrown
  // hundreds of thousands of pixels back up the transcript and left there.
  let worst = arrived;
  for (let watched = 0; watched < WATCH_MS; watched += 250) {
    await page.waitForTimeout(250);
    const now = await where();
    if (now.fromEnd > worst.fromEnd) worst = now;
    if (now.fromEnd > AT_END) {
      console.error(
        `the reader was displaced ${Math.round(watched / 100) / 10}s after the room settled:
  at the end (${arrived.fromEnd}px short) -> ${now.fromEnd}px short, scrollTop ${arrived.top} -> ${now.top}.
  Nothing arrived and nobody scrolled in that window - the row count and the height did not move.
  A reader who has not scrolled must not be moved by anything except new messages.`,
      );
      process.exit(1);
    }
  }

  // Told about messages nobody sent. The history the view was still drawing
  // counted as unread is the same stale reading, seen from the other side, and
  // a version that holds position while claiming 319 new messages is not fixed.
  const pill = await page.locator("main button", { hasText: "jump to latest" });
  if ((await pill.count()) > 0) {
    console.error(
      `the view held its place but offers "${(await pill.first().textContent())?.trim()}" over a room nobody has posted to.
  That count is the room's own history, arriving in pages, counted as new.`,
    );
    process.exit(1);
  }

  console.log(
    `ok  the room landed at the end and stayed: ${rowsSettled} rows over ${Math.ceil(rowsSettled / PAGE)} pages, ` +
      `worst drift ${worst.fromEnd}px over ${WATCH_MS / 1000}s, no pill`,
  );
} finally {
  await browser.close();
}
