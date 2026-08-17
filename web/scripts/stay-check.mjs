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
 * The pill is asserted too, and it is not a nicety here: "N new messages" over
 * a room nobody has touched means the view counted history it was still
 * drawing as unread, which is the same stale reading seen from the other side.
 *
 * AND THE CUTOFF, which is the same complaint answered at the source. The room
 * opens on a bounded window now rather than reading itself from the beginning
 * and paging the whole history in behind the poll - reported by the operator as
 * "on reload the whole chat history loads". So the seed is still MORE THAN ONE
 * PAGE, and what it now proves is the opposite of what it used to: the room
 * holds far more than the view fetched.
 *
 * The history is still reachable, and reaching it must not move the reader. The
 * last part of this check scrolls to the top, waits for the page before the
 * window to arrive, and asserts THE SAME MESSAGE IS STILL WHERE IT WAS - the
 * prepend pushes everything on screen down by the height of what arrived, and a
 * reader thrown a window up the room by their own scroll is the reported bug
 * again, just triggered deliberately rather than by a poll.
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
 * A page of chat is 200 rows (store.defaultLimit) and the room opens on a
 * window smaller than that, so a seed of two and a bit pages is a room that
 * comfortably outgrows what the view fetches - which is the cutoff this now
 * asserts, and enough history to page back through afterwards.
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
  // THE CUTOFF. The room holds `inTheRoom` messages and the view settled on
  // `rowsSettled` of them, so this is the operator's complaint stated as a
  // number: a room does not load its whole history to be read.
  //
  // The room is read back rather than assumed from SEED, because a re-run does
  // not re-seed and another check sharing the node could have said something
  // here. What the assertion needs is that the room is BIGGER than the window,
  // and only the node can say how big it is.
  const inTheRoom = await held();
  if (rowsSettled === 0) {
    console.error("nothing rendered at all, so nothing about scrolling was tested");
    process.exit(1);
  }
  if (inTheRoom <= PAGE) {
    console.error(
      `#${room} holds ${inTheRoom} messages, which is not more than one page (${PAGE}).
  A room no bigger than a page proves nothing about a cutoff - seed more.`,
    );
    process.exit(1);
  }
  if (rowsSettled >= inTheRoom) {
    console.error(
      `the room loaded its whole history: ${rowsSettled} rows on screen out of ${inTheRoom} in the room.
  Opening a room fetches a bounded window and pages back on demand - see ChatRoom.CHAT_WINDOW.`,
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

  // AND THE HISTORY IS STILL THERE, AND REACHING IT DOES NOT MOVE THE READER.
  //
  // Scrolling to the top asks for the page before the window. That page is
  // prepended, so every message on screen is pushed down by the height of what
  // arrived - and if the view does nothing about it, the reader is thrown a
  // window's worth of room away from the line they were reading. That is the
  // reported bug again, reached by scrolling instead of by waiting.
  //
  // The anchor is taken INSIDE the same evaluate that scrolls, so the reading
  // is the DOM before the fetch: setting scrollTop cannot resolve a request
  // synchronously, so nothing can have landed between the two lines.
  const anchor = await scroller.evaluate((el) => {
    el.scrollTop = 0;
    const top = el.getBoundingClientRect().top;
    const seen = [...el.querySelectorAll("[data-body]")];
    const row = seen.find((r) => r.getBoundingClientRect().bottom > top) || seen[0];
    if (!row) return null;
    return {
      id: row.getAttribute("data-body"),
      at: Math.round(row.getBoundingClientRect().top - top),
    };
  });
  if (!anchor) {
    console.error("no message rows to hold on to at the top of the transcript");
    process.exit(1);
  }

  let grew = 0;
  for (let waited = 0; waited < 10_000; waited += 200) {
    grew = await rows();
    if (grew > rowsSettled) break;
    await page.waitForTimeout(200);
  }
  if (grew <= rowsSettled) {
    console.error(
      `scrolling to the top of #${room} fetched nothing: still ${grew} rows, and the room holds ${inTheRoom}.
  A bounded window is only acceptable because the rest arrives when somebody scrolls up for it.`,
    );
    process.exit(1);
  }

  // Where that same message is now. It must be where it was, near enough that
  // nobody's eye moved - the tolerance is the app's own idea of "did not move".
  const moved = await scroller.evaluate((el, id) => {
    const row = el.querySelector(`[data-body="${id}"]`);
    if (!row) return null;
    return Math.round(row.getBoundingClientRect().top - el.getBoundingClientRect().top);
  }, anchor.id);
  if (moved === null) {
    console.error(
      `the message the reader was on (${anchor.id}) is not on screen after paging back`,
    );
    process.exit(1);
  }
  if (Math.abs(moved - anchor.at) > AT_END) {
    console.error(
      `paging back moved the reader: the message they were on was ${anchor.at}px into the view and is now ${moved}px.
  Older messages are prepended, so the view has to put the reader back on the line they were reading
  before the browser paints - see the layout effect in MessageList.`,
    );
    process.exit(1);
  }

  // And the page it fetched is not offered as news. History the reader asked
  // for, counted as unread, is the "319 new messages" bug wearing the other hat.
  if ((await pill.count()) > 0) {
    console.error(
      `paging back offers "${(await pill.first().textContent())?.trim()}" over messages OLDER than everything on screen.`,
    );
    process.exit(1);
  }

  console.log(
    `ok  #${room} holds ${inTheRoom}, opened on ${rowsSettled}, stayed at the end ` +
      `(worst drift ${worst.fromEnd}px over ${WATCH_MS / 1000}s, no pill), ` +
      `and paging back to ${grew} rows moved the reader ${Math.abs(moved - anchor.at)}px`,
  );
} finally {
  await browser.close();
}
