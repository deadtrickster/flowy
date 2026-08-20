/**
 * A room in the sidebar looks like the other things in the sidebar.
 *
 *   node scripts/sidebar-consistent-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR SENT A SCREENSHOT captioned "note the fonts": the room names
 * were rendering at the page's base size, and the hash and the name had stacked
 * one above the other, so three rooms filled the sidebar.
 *
 * The cause was one call. navClass is a FUNCTION - NavLink hands it isActive -
 * and Shell.tsx passed it to cn() as if it were a string. clsx ignores
 * functions, silently, so the room link rendered with the two classes added
 * beside it and NONE of the ones navClass carries. No text-sm, so 16px instead
 * of 14px. No flex, so two block-level children.
 *
 * SO THIS COMPARES, rather than asserting a class list. A class list is the
 * thing that broke, and a check that reads it would have been written from the
 * same wrong assumption - that the string ever arrived. The property worth
 * keeping is that a room and a nav item are the same size and lay out the same
 * way, and the browser is the only honest witness to that.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/sidebar-consistent-check.mjs BASE_URL TOKEN");
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
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await page
    .locator("[data-room-list]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);

  const seen = await page.evaluate(() => {
    const read = (el) =>
      el
        ? {
            font: getComputedStyle(el).fontSize,
            display: getComputedStyle(el).display,
            lines: el.getClientRects().length,
            text: (el.textContent || "").trim().slice(0, 20),
          }
        : null;
    return {
      room: read(document.querySelector('[data-room-list] a[href^="/chat/"]')),
      // /profile is a plain nav item using navClass directly, so it is what a
      // room is supposed to match.
      nav: read(document.querySelector('a[href="/profile"]')),
      rooms: document.querySelectorAll('[data-room-list] a[href^="/chat/"]').length,
    };
  });

  if (!seen.nav) die("no /profile nav item to compare a room against");
  if (!seen.room || seen.rooms === 0) die("no rooms in the sidebar - nothing was compared");

  if (seen.room.font !== seen.nav.font) {
    die(`a room renders at ${seen.room.font} and a nav item at ${seen.nav.font}`);
  }
  if (seen.room.display !== seen.nav.display) {
    die(`a room lays out as ${seen.room.display} and a nav item as ${seen.nav.display}`);
  }
  // One line. The stacking is what made three rooms fill the sidebar, and it is
  // visible as a second client rect rather than in any class.
  if (seen.room.lines > 1) {
    die(`the room ${JSON.stringify(seen.room.text)} wraps onto ${seen.room.lines} lines`);
  }

  console.log(
    `${seen.rooms} rooms at ${seen.room.font} ${seen.room.display}, the same as a nav item, one line each`,
  );
} finally {
  await browser.close();
}
