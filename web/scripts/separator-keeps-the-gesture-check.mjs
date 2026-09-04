/**
 * THE NAV SEPARATOR KEEPS A TOUCH GESTURE, AND THE COLUMN CAN BE PUT AWAY.
 *
 *   node scripts/separator-keeps-the-gesture-check.mjs BASE_URL TOKEN
 *
 * The operator, from a Fold 8: "i try to drag the separator and it does follow
 * for some pixels and then stops, so i have to do it multiple times... also
 * please make it collapsible".
 *
 * WHAT ACTUALLY BROKE, and it is not pointer capture. The handle already used
 * pointer events and setPointerCapture, which is why it read as correct. With
 * the default touch-action a touch browser spends the opening pixels of a
 * gesture deciding whether it is a scroll; when it decides yes it takes the
 * gesture and fires pointercancel, and the handle's own onPointerCancel then
 * clears `active`. That is "follows for some pixels and then stops" exactly.
 * Capture cannot help - it routes events this element would receive, and the
 * browser has stopped sending any. Only touch-action:none stops the browser
 * arbitrating at all.
 *
 * WHY THIS ASSERTS THE PROPERTY AND NOT A SIMULATED FLING. Whether a browser
 * steals a gesture is decided by its own scroll heuristics against real touch
 * input; Playwright's touchscreen API taps, and CDP-dispatched touch sequences
 * do not reproduce that arbitration faithfully. A check that mimed a drag and
 * passed would be saying nothing about the device the report came from. So the
 * arm is the switch that decides the behaviour, read from the computed style of
 * the element the finger lands on - which goes red on exactly the change that
 * caused the bug and cannot pass while the default is in force.
 *
 * MEASURED AT FOLD WIDTHS, both of them. The handle only exists at md and up,
 * so the folded width is the arm that proves the control is reachable at all
 * and the unfolded one is where the operator was dragging.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/separator-keeps-the-gesture-check.mjs BASE_URL TOKEN");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  for (const [name, width, height] of [
    ["an unfolded fold", 880, 1100],
    ["a wide window", 1600, 1000],
  ]) {
    const page = await browser.newPage({
      viewport: { width, height },
      isMobile: true,
      hasTouch: true,
    });
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/`, { timeout: 30_000 });
    await page
      .locator('[data-resize-handle="flowy.nav.width"]')
      .first()
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => {});

    const handle = page.locator('[data-resize-handle="flowy.nav.width"]').first();
    if ((await handle.count()) === 0) {
      die(`at ${width}px (${name}) there is no nav separator on screen at all, so there is
nothing to drag. It is drawn from md upward; if the breakpoint moved, this arm moved with it.`);
    }

    // THE SWITCH THAT DECIDES WHETHER THE BROWSER MAY TAKE THE GESTURE.
    const touchAction = await handle.evaluate((el) => getComputedStyle(el).touchAction);
    if (touchAction !== "none") {
      die(`at ${width}px (${name}) the nav separator computes touch-action:${touchAction}.
Anything but "none" lets the browser arbitrate the opening pixels of a gesture and hand
them to a scroller, which is the drag that "follows for some pixels and then stops".
setPointerCapture does not cover this: it routes events the element would get, and a
stolen gesture stops sending them.`);
    }

    // AND THE TARGET IS ONE A FINGER CAN FIND. Six pixels is a mouse target.
    const box = await handle.boundingBox();
    if (!box || box.width < 12) {
      die(`at ${width}px (${name}) the separator's grab area is ${
        box ? Math.round(box.width) : "no"
      }px wide on a touch screen. A finger is about 9mm; a target this narrow is hit by luck,
which is the other half of "cumbersome".`);
    }

    // THE COLUMN CAN BE PUT AWAY AND BROUGHT BACK, and the control that brings
    // it back has to be on screen while it is away - a panel with no way back
    // is the failure the handle's own bounds already refuse.
    const aside = page.locator("[data-nav]").first();
    const before = (await aside.boundingBox())?.width ?? 0;
    const toggle = page.getByRole("button", { name: /navigation column/ }).first();
    if ((await toggle.count()) === 0) {
      die(`at ${width}px (${name}) there is no control to collapse the navigation column.`);
    }
    await toggle.click({ timeout: 10_000 });
    await page.waitForTimeout(400);
    const after = (await aside.boundingBox())?.width ?? 0;
    if (!(after < before && after < 8)) {
      die(`at ${width}px (${name}) collapsing left the column ${Math.round(after)}px wide,
down from ${Math.round(before)}px. Collapsed has to mean gone, or the control says one
thing and the layout another.`);
    }

    const back = page.getByRole("button", { name: /show the navigation column/ }).first();
    if ((await back.count()) === 0) {
      die(`at ${width}px (${name}) the column collapsed and nothing on screen brings it back.
That is the panel-with-no-way-back the width bounds exist to prevent, reached by a
different route.`);
    }
    await back.click({ timeout: 10_000 });
    await page.waitForTimeout(400);
    const restored = (await aside.boundingBox())?.width ?? 0;
    if (restored < 8) {
      die(`at ${width}px (${name}) the column did not come back: still ${Math.round(restored)}px.`);
    }

    console.log(
      `${name} (${width}px): touch-action=none, ${Math.round(box.width)}px target, collapse ${Math.round(
        before,
      )} -> ${Math.round(after)} -> ${Math.round(restored)}`,
    );
    await page.close();
  }
} finally {
  await browser.close();
}
