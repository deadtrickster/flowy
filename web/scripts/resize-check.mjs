/**
 * The two columns can be dragged, keep their bounds, and remember.
 *
 *   node scripts/resize-check.mjs BASE_URL TOKEN
 *
 * The operator: "make left/right panels resizable". Both columns were a fixed
 * width chosen by whoever wrote them - w-60 and w-[26rem].
 *
 * EVERY ASSERTION IS A MEASURED WIDTH, not a class name. Three of the four bugs
 * this check found while it was being written would have passed a source read:
 *
 *   two width utilities at one breakpoint - md:w-60 beside md:w-[var(--nav-w)] -
 *   and Tailwind's emission order decided, not the attribute order. The drag ran
 *   and the panel did not move.
 *
 *   the move handler read `dragging` STATE, which is one render behind the
 *   pointerdown that set it, so the opening frames of every drag were dropped.
 *   The ref is what the handler reads now; the state only repaints.
 *
 *   the handle was w-px with a pseudo-element pretending to be a hit area, and a
 *   pointer aimed at the middle of it hit NOTHING - the element itself recorded
 *   zero pointerdowns. A target you can only hit by accident is the same defect
 *   as a control clipped by a scroll container, which this console has had twice.
 *
 * The fourth was in the check rather than the code: the first version cleared
 * the stored widths in addInitScript, which runs on the RELOAD too, and so
 * "proved" that persistence was broken.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/resize-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  // The token only. Anything cleared here is cleared on the reload as well.
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 });
  await page.evaluate(() => {
    localStorage.removeItem("flowy.nav.width");
    localStorage.removeItem("flowy.roompanel.width");
  });
  await page.reload({ timeout: 20_000 });

  const nav = page.locator("[data-nav]");
  const panel = page.locator("[data-room-panel-state]");
  await nav.waitFor({ state: "visible", timeout: 20_000 });
  const width = async (locator) => {
    const box = await locator.boundingBox();
    if (!box) die("a column this check is about is not on the page");
    return Math.round(box.width);
  };

  // THE DEFAULT IS WHAT IT ALWAYS WAS. A reader who never drags anything must
  // see no change at all, and nothing may be stored on their behalf.
  const navWas = await width(nav);
  const panelWas = await width(panel);
  if (navWas !== 240 || panelWas !== 416) {
    die(
      `the untouched columns are ${navWas} and ${panelWas}, not the 240 and 416 they have always been`,
    );
  }

  const drag = async (key, dx) => {
    const handle = page.locator(`[data-resize-handle="${key}"]`);
    await handle.waitFor({ state: "visible", timeout: 10_000 });
    const box = await handle.boundingBox();
    if (!box || box.width < 4) {
      die(
        `the ${key} handle is ${box ? box.width : "missing"}px wide - too thin to hit on purpose`,
      );
    }
    await page.mouse.move(box.x + box.width / 2, box.y + 200);
    await page.mouse.down();
    await page.mouse.move(box.x + dx, box.y + 200, { steps: 8 });
    await page.mouse.up();
    await page.waitForTimeout(150);
  };

  await drag("flowy.nav.width", 110);
  const navWide = await width(nav);
  if (navWide <= navWas) die(`dragging the nav edge right left it at ${navWide}, from ${navWas}`);

  await drag("flowy.roompanel.width", -140);
  const panelWide = await width(panel);
  if (panelWide <= panelWas) {
    die(`dragging the panel edge left left it at ${panelWide}, from ${panelWas}`);
  }

  // IT REMEMBERS. localStorage, deliberately - a column width is a fact about
  // THIS screen, unlike a reader mark, which lib/unread.tsx keeps on the node
  // precisely so it cannot differ per device.
  await page.reload({ timeout: 20_000 });
  await nav.waitFor({ state: "visible", timeout: 20_000 });
  const navBack = await width(nav);
  const panelBack = await width(panel);
  if (navBack !== navWide || panelBack !== panelWide) {
    die(
      `after a reload the columns are ${navBack} and ${panelBack}, not the ${navWide} and ${panelWide} they were dragged to`,
    );
  }

  // AND THEY CANNOT BE DRAGGED AWAY. A column at zero is a column nobody can
  // get back, and the floor is the whole reason the bounds are arguments.
  await drag("flowy.nav.width", -900);
  const navFloor = await width(nav);
  if (navFloor < 180) die(`the nav was dragged to ${navFloor}px, under its 180 floor`);
  await drag("flowy.roompanel.width", 900);
  const panelFloor = await width(panel);
  if (panelFloor < 280) die(`the panel was dragged to ${panelFloor}px, under its 280 floor`);

  // THE KEYBOARD REACHES IT TOO. A drag only a mouse can perform hands the
  // layout to one kind of reader.
  const handle = page.locator('[data-resize-handle="flowy.nav.width"]');
  await handle.focus();
  await page.keyboard.press("End");
  await page.waitForTimeout(150);
  const navMax = await width(nav);
  if (navMax <= navFloor) {
    die(`End on the focused handle left the nav at ${navMax} - the keyboard cannot move it`);
  }

  console.log(
    `nav ${navWas}->${navWide}, panel ${panelWas}->${panelWide}, both survived a reload, ` +
      `floors held at ${navFloor} and ${panelFloor}, and End took the nav to ${navMax}`,
  );
} finally {
  await browser.close();
}
