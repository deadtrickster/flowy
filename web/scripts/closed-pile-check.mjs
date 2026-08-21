/**
 * The pile of closed rooms has a bottom.
 *
 *   node scripts/closed-pile-check.mjs BASE_URL TOKEN
 *
 * The operator: "I hope closed rooms accordeon does lazy loading, paginated by
 * scroll". MEASURED before building: 29 rooms on the dogfood node, so the worst
 * case is 29 buttons - not a rendering cost. What is real is a LAYOUT cost: the
 * open list scrolls inside itself and the closed pile did not, so opening it
 * pushed the rail's own footer - the token box, the log out - below the fold.
 *
 * So the assertion is not "it renders fewer": it is that the pile is BOUNDED and
 * scrolls itself, and that what sits under it does not move when it opens. A
 * check that only counted rendered rooms would pass on the version that shoves
 * the footer off the screen, which is the version the operator was looking at.
 *
 * It closes rooms through the console's own door - a personal note on the node,
 * per principal - and puts them back at the end.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/closed-pile-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const die = (message) => {
  console.error(message);
  process.exit(1);
};

// Enough rooms to overflow any sane bound. They are named rather than taken
// from the node's list so this does not depend on what else has been said
// tonight, and saying in a room is what makes it exist.
const rooms = Array.from({ length: 40 }, (_, i) => `closedpile${i}`);
for (const room of rooms) {
  const res = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ body: "closed-pile-check" }),
  });
  if (!res.ok) die(`could not seed ${room}: ${res.status}`);
}

// CLOSED THROUGH THE DOOR THE CONSOLE USES, not by clicking twenty-four times.
//
// The first version clicked each room's close control and hung: the buttons sit
// in a scrolling column that re-lays out as rooms leave it, so click twenty-five
// was waiting on an element that had moved. The gesture is not what this check
// is about - the RENDER is - and the console reads its closed list from a
// personal note, so writing that note is the same state by a shorter road.
const closeAll = async () => {
  const held = await fetch(
    `${base}/api/artifacts?type=memory&kind=note&tag=console-hidden-rooms&limit=5`,
    { headers: bearer },
  );
  const note = held.ok ? ((await held.json()).artifacts ?? [])[0] : null;
  const res = await fetch(`${base}/api/artifacts`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({
      ...(note ? { id: note.id } : {}),
      type: "memory",
      kind: "note",
      title: "console: rooms I have closed",
      body: JSON.stringify(rooms),
      visibility: "personal",
      tags: ["console-hidden-rooms"],
    }),
  });
  if (!res.ok) die(`could not close the rooms: ${res.status} ${await res.text()}`);
};

await closeAll();

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 30_000 });
  await page.locator("nav").first().waitFor({ state: "visible", timeout: 30_000 });

  const pile = page.locator("[data-closed-rooms]");
  await pile.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await pile.count()) === 0) die("nothing was closed, so there is no pile to measure");

  // OPENED BY SETTING THE ATTRIBUTE, not by clicking the summary. The accordion
  // sits at the end of a column that scrolls, and Playwright's click waits for
  // the target to stop moving - which it does not while the column settles
  // around twenty-four new rows. The gesture is not what this measures; the
  // BOX is. A check for the click belongs with the close control, not here.
  await pile.evaluate((el) => {
    el.open = true;
  });
  await page.waitForTimeout(500);

  const list = page.locator("[data-closed-rooms-list]");
  if ((await list.count()) === 0) {
    die(
      "the closed pile has no bounded list - see Shell, it renders every room straight into the accordion",
    );
  }
  const box = await list.boundingBox();
  const height = box?.height ?? 0;
  const viewport = page.viewportSize()?.height ?? 0;
  if (height === 0) die("the closed list has no height, so nothing was measured");
  // HALF THE WINDOW IS THE BAR, and the number matters: the bound is 40vh, so a
  // bounded pile is ~320px in an 800px window and an unbounded one with forty
  // rooms in it is ~800. The first draft used 0.6 and the unbounded case landed
  // at exactly 480 - it failed through the "not measuring a bound" arm below
  // instead, which is a red for the wrong reason and would have hidden a real
  // regression behind a confusing message.
  if (height > viewport * 0.5) {
    die(`the closed pile is ${Math.round(height)}px in a ${viewport}px window - it is not bounded,
so opening it pushes whatever is under it in the rail off the screen.`);
  }

  // AND IT SCROLLS ITSELF rather than pushing the rail: the content is taller
  // than the box, and the box is what moves.
  const overflow = await list.evaluate((el) => el.scrollHeight - el.clientHeight);
  if (overflow <= 0) {
    die(`the pile fits its box (${Math.round(height)}px) with ${rooms.length} rooms closed,
so this check is not measuring a bound - seed more rooms.`);
  }
  await list.evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  const scrolled = await list.evaluate((el) => el.scrollTop);
  if (scrolled <= 0) die("the closed pile does not scroll inside itself");

  console.log(
    `${rooms.length} rooms closed: the pile is ${Math.round(height)}px in a ${viewport}px window, ` +
      `scrolls ${Math.round(overflow)}px inside itself`,
  );
} finally {
  // PUT THEM BACK. A check that leaves two dozen rooms closed leaves the next
  // reader's sidebar rearranged - the same rule as the rows: what a fixture
  // changes, it changes back.
  const held = await fetch(`${base}/api/artifacts?type=memory&kind=note&tag=flowy:hidden-rooms`, {
    headers: bearer,
  });
  if (held.ok) {
    const note = ((await held.json()).artifacts ?? [])[0];
    if (note) {
      await fetch(`${base}/api/artifacts`, {
        method: "POST",
        headers: { ...bearer, "Content-Type": "application/json" },
        body: JSON.stringify({
          id: note.id,
          type: "memory",
          kind: "note",
          title: note.title,
          body: "[]",
          tags: note.tags,
          visibility: note.visibility,
        }),
      }).catch(() => {});
    }
  }
  await browser.close();
}
