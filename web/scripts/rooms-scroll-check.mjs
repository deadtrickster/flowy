/**
 * The rooms list scrolls inside itself, and the page does not.
 *
 *   node scripts/rooms-scroll-check.mjs BASE_URL TOKEN
 *
 * Raised by the operator: "rooms list renders whole list way down - without its
 * own scrollbar - the whole page strats scrolling". Measured before the fix:
 * the aside carried no height bound and no overflow, so a node with more rooms
 * than fit made the sidebar taller than the viewport and took the whole
 * document with it - and the token bar at the bottom of the sidebar went off
 * the end of the page with it.
 *
 * WHY THIS ASSERTS GEOMETRY AND NOT CLASS NAMES. "the element has
 * overflow-y-auto" is a fact about the source, and the source was not the
 * problem: a flex child whose min-height is its content never gets a box
 * smaller than its list, so the overflow property is set and does nothing. The
 * only honest question is whether the box is shorter than what is in it and
 * whether the DOCUMENT is taller than the window, both of which the browser
 * will answer.
 *
 * It needs more rooms than fit, so it makes them: a room exists here as soon as
 * somebody speaks in one, and useRooms reads the node rather than a list in the
 * source.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/rooms-scroll-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const say = async (room, text) => {
  const res = await fetch(`${base}/api/chat/${room}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ body: text }),
  });
  if (!res.ok) die(`could not speak in ${room}: ${res.status} ${await res.text()}`);
};

// ENOUGH TO OVERFLOW A SHORT WINDOW, not a round number picked for looks. The
// viewport below is 900px tall and a room row is about 32px, so thirty rooms
// cannot fit beside the fifteen fixed nav entries however the spacing changes.
const rooms = [];
for (let i = 0; i < 30; i++) rooms.push(`scrollcheck-${i.toString(36)}`);
for (const room of rooms) await say(room, "a room exists once somebody has spoken in it");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await page.locator('a[href^="/chat/scrollcheck-"]').first().waitFor({ timeout: 20_000 });

  // THE WITNESS THAT THIS RUN MEASURED ANYTHING. If the rooms never rendered,
  // every assertion below would pass for the wrong reason: no overflow anywhere.
  const shown = await page.locator('a[href^="/chat/scrollcheck-"]').count();
  if (shown < 20) {
    die(`only ${shown} of the ${rooms.length} rooms rendered - nothing here would overflow`);
  }

  const geometry = await page.evaluate(() => {
    const link = document.querySelector('a[href^="/chat/scrollcheck-"]');
    // The scrolling ancestor of a room link, whatever the markup calls it.
    let box = link?.parentElement ?? null;
    while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
    return {
      docScrolls: document.documentElement.scrollHeight > window.innerHeight + 1,
      found: box ? box.className : null,
      isBody: box === document.body || box === document.documentElement,
      inner: box?.scrollHeight ?? 0,
      outer: box?.clientHeight ?? 0,
    };
  });

  if (geometry.docScrolls) {
    die("the document is taller than the window - the page scrolls, which is the report itself");
  }
  if (!geometry.found || geometry.isBody) {
    die(
      "no ancestor of a room link scrolls - the list is either not overflowing or " +
        "it is overflowing something that cannot scroll",
    );
  }
  if (geometry.inner <= geometry.outer) {
    die(
      `the scrolling box is ${geometry.inner}px of content in ${geometry.outer}px - it is not scrolling`,
    );
  }

  // AND THE TOKEN BAR IS STILL ON SCREEN. The failure the operator saw was not
  // only that the page scrolled: what the sidebar pushed off the bottom was the
  // way to change your token. A list that scrolls while the thing under it is
  // gone has moved the problem rather than fixed it.
  const bar = page.locator("[data-token-bar]").first();
  if ((await bar.count()) === 0) {
    console.log("no [data-token-bar] to check - skipping the last assertion");
  } else {
    const box = await bar.boundingBox();
    if (!box || box.y + box.height > 900 + 1) {
      die(`the token bar sits at y=${box?.y ?? "nowhere"} in a 900px window - it is off screen`);
    }
  }

  console.log(
    `${shown} rooms, ${geometry.inner}px of list in a ${geometry.outer}px box, document does not scroll`,
  );
} finally {
  await browser.close();
}
