/**
 * What a person actually sees, in a real browser, against the live node.
 *
 * render-check.mjs already paints the shipped bundle in jsdom, which catches a
 * console that throws on mount. This one is a layer past that: a real engine,
 * real layout, real event loop, and assertions on ELEMENTS rather than on the
 * page's text.
 *
 * The element part is the lesson, not a detail. Checking the room's todo panel
 * by searching the page for "todos" passes with the panel entirely absent,
 * because the word is also in the global navigation - which is exactly what
 * happened the first time this was checked by hand. A string that appears in
 * two places is not evidence about either. So: find the panel, then read it.
 *
 *   node scripts/browser-check.mjs BASE_URL TOKEN EXPECTED_TEXT
 *
 * EXPECTED_TEXT has to appear INSIDE the room's todo panel, which is the aside
 * section headed "todos" - not anywhere on the page.
 */

import { chromium } from "playwright";

const [base, token, expected] = process.argv.slice(2);

if (!base || !token || !expected) {
  console.error("usage: node scripts/browser-check.mjs BASE_URL TOKEN EXPECTED_TEXT");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  const panel = page
    .locator("aside section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();

  try {
    await panel.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `the room has no todo panel: no aside section headed "todos".
The word appears in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }

  // Wait for the ROW, not for the panel. The panel is visible from mount with
  // an empty list, and its todos arrive one fetch later - so reading its text
  // the moment it appears asserts on the empty state and fails a feature that
  // works. That is what this check did first: the API had all thirteen and the
  // assertion saw none of them, because it asked too early rather than because
  // anything was wrong.
  try {
    await panel.getByText(expected, { exact: false }).first().waitFor({
      state: "visible",
      timeout: 15_000,
    });
  } catch {
    const shown = await panel.innerText();
    console.error(`the room's todo panel does not show ${JSON.stringify(expected)}. It shows:
${shown}`);
    process.exit(1);
  }

  console.log(`the room's todo panel shows ${JSON.stringify(expected)}, in a browser`);
} finally {
  await browser.close();
}
