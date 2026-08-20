/**
 * The console is usable on a phone, and unchanged on a desk.
 *
 *   node scripts/phone-check.mjs BASE_URL TOKEN
 *
 * WHAT WAS WRONG, measured in a real browser against the deployed node on
 * 2026-08-20 after the operator said "can open general room because not enough
 * vertical space on the phone - simply not visible":
 *
 *   390x664   aside 240  main 150  composer 26px wide  rooms on screen 0/28
 *   768x1024  aside 240  main 528                      rooms 10/28
 *   1600x1000 aside 240  main 1360 composer 920
 *
 * Two defects behind one complaint. Shell.tsx had no breakpoint anywhere, so
 * the 240px nav took 62% of a phone and its thirteen links pushed the ROOMS
 * heading below the fold - not one of twenty-eight rooms reachable. And in the
 * 150px left over, ChatRoom's w-[26rem] shrink-0 side column did not shrink, so
 * the todos pane, the thread pane and the transcript were painted ON TOP of one
 * another. Neither announced itself: the page does not overflow horizontally,
 * so nothing said anything was missing.
 *
 * BOTH DIRECTIONS, because "it changed at 390px" is also true of a console that
 * is now broken at 1600. The desktop arm asserts the drawer button is absent
 * and the panel is a column - the layout this console has always had.
 *
 * GEOMETRY, NOT CLASS NAMES, which is rooms-scroll-check.mjs's lesson and
 * applies twice as hard here: `md:static` is a fact about the source, and a
 * source that says the right thing while the browser lays it out wrong is
 * exactly what shipped. Everything below is read off getBoundingClientRect.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/phone-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

/** onScreen is whether a box is inside the window on every side. */
const onScreen = (box, w, h) =>
  box && box.x >= -1 && box.y >= -1 && box.x + box.width <= w + 1 && box.y + box.height <= h + 1;

const browser = await chromium.launch();
try {
  // ---------------------------------------------------------------- the phone
  const phone = await browser.newPage({ viewport: { width: 390, height: 664 } });
  await phone.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await phone.goto(`${base}/chat/general`, { timeout: 20_000 });
  // WAITED FOR, NOT SLEPT THROUGH. Every wait in this file was a fixed
  // waitForTimeout when it was written - 1200, 400, 300, 400 - and both halves
  // of that are wrong. Slow, because the sleep runs in full on a machine that
  // was ready in 80ms, and 38 such sleeps across web/scripts total 70 seconds
  // of a 530 second gate. Flaky, because a loaded box takes longer than the
  // guess and the assertion lands early - which is exactly how way-in-check
  // failed tonight, reporting "the login page has no password field" against a
  // page that has had one since it was written.
  //
  // The composer is the last thing this view paints, so its presence is the
  // condition the sleep was standing in for.
  await phone.locator("textarea").first().waitFor({ state: "visible", timeout: 20_000 });

  const width = 390;
  const height = 664;

  // THE TRANSCRIPT GETS THE SCREEN. main used to be 150px of 390 because the
  // nav was still holding 240 of it.
  const main = await phone.locator("main").first().boundingBox();
  if (!main || main.width < width * 0.9) {
    die(`main is ${main ? Math.round(main.width) : "missing"}px of a ${width}px phone`);
  }

  // AND SOMETHING TO TYPE IN. 26px was the number that made this a bug report
  // rather than a preference.
  const composer = await phone.locator("textarea").first().boundingBox();
  if (!composer || composer.width < width * 0.5) {
    die(
      `the box you type in is ${composer ? Math.round(composer.width) : "missing"}px wide on a ${width}px phone`,
    );
  }

  // NOTHING RUNS OFF THE SIDE. A horizontal scroll would at least be a signal;
  // its absence is why the old layout just looked broken.
  const scrollW = await phone.evaluate(() => document.documentElement.scrollWidth);
  if (scrollW > width + 1) die(`the page is ${scrollW}px wide in a ${width}px window`);

  // THE ROOMS ARE REACHABLE, which is the operator's actual complaint. Not
  // "the list exists" - a room link fully inside the window, after one press.
  const opener = phone.locator("[data-nav-open]").first();
  if ((await opener.count()) === 0) die("there is no way to open the navigation on a phone");
  if (!(await opener.isVisible())) die("[data-nav-open] is in the document and not visible");
  await opener.click();
  // OPEN IS THE BACKDROP EXISTING, not a link being "visible".
  //
  // This check went RED against a fixture with 52 rooms, reporting "the drawer
  // is open, holds 52 rooms, and not one of them is on screen" - and the drawer
  // was not open at all. The version before it waited for the first link to be
  // VISIBLE, which Playwright answers yes to for an element translated
  // off-canvas: -translate-x-full moves the drawer out of the viewport and
  // leaves every child with a real bounding box, no display:none and no
  // visibility:hidden. So the wait returned at once, on a closed drawer, and
  // every link then measured at x=-288.
  //
  // I wrote that wait to replace a 400ms sleep, and it is worse than the sleep:
  // the sleep at least let the transform finish. A wait that returns
  // immediately and proves nothing is not an improvement on waiting for time
  // to pass - it is the same defect with a better name on it.
  //
  // The backdrop is rendered CONDITIONALLY on the open state, so its presence
  // is a fact about the drawer rather than about where a transform has got to.
  // Its detachment is what "closed" waits on below, for the same reason.
  await phone.locator("[data-nav-backdrop]").waitFor({ state: "attached", timeout: 10_000 });
  const links = phone.locator('[data-nav] a[href^="/chat/"]');
  // AND THEN THE TRANSFORM. Open is decided; this waits for the drawer to have
  // arrived where it is going, by asking WHERE IT IS rather than whether the
  // browser considers it visible.
  await phone
    .waitForFunction(
      () => {
        const el = document.querySelector("[data-nav]");
        return !!el && el.getBoundingClientRect().x >= 0;
      },
      undefined,
      { timeout: 10_000 },
    )
    .catch(() => {});

  const total = await links.count();
  if (total === 0) die("the drawer opened and holds no rooms");
  let reachable = 0;
  for (let i = 0; i < total; i++) {
    if (onScreen(await links.nth(i).boundingBox(), width, height)) reachable++;
  }
  if (reachable === 0) {
    die(
      `the drawer is open, holds ${total} rooms, and not one of them is on screen - which is the state this check was written for`,
    );
  }

  // THE DRAWER IS OPAQUE. It shipped at bg-card/40 for one build and the
  // transcript read straight through it: two layers of text in the same place,
  // which is unreadable in a way no geometry assertion would have caught.
  const drawerBg = await phone.evaluate(() => {
    const el = document.querySelector("[data-nav]");
    return el ? getComputedStyle(el).backgroundColor : "";
  });
  const alpha = /rgba?\([^)]*?,\s*([0-9.]+)\s*\)$/.exec(drawerBg);
  if (alpha && Number(alpha[1]) < 0.95) {
    die(`the drawer's background is ${drawerBg} - the page behind it shows through`);
  }
  await opener.click();
  // CLOSED IS THE BACKDROP GOING AWAY, not the links going invisible. The
  // drawer closes with a transform, and an element translated off-canvas still
  // has a bounding box - Playwright would call it visible forever and this
  // would time out. The backdrop is rendered conditionally, so its detachment
  // is a fact rather than an appearance.
  await phone.locator("[data-nav-backdrop]").waitFor({ state: "detached", timeout: 10_000 });

  // THE ROOM'S PANEL IS REACHABLE AND FITS. This is the overlap half.
  const toggle = phone.locator("[data-room-panel-toggle]").first();
  if ((await toggle.count()) === 0) die("there is no way to reach the room panel on a phone");
  await toggle.click();
  // THE SAME TRAP AS THE DRAWER, and I walked into it twice in one file: the
  // panel slides in with translate-x-full, so "visible" is true of it while it
  // is still entirely off the right-hand side. The state attribute is rendered
  // from the open flag, so it is a fact; the position is then waited for by
  // asking where the box actually is.
  const panelEl = phone.locator('[data-room-panel-state="open"]').first();
  await panelEl.waitFor({ state: "attached", timeout: 10_000 });
  await phone
    .waitForFunction(
      (w) => {
        const el = document.querySelector('[data-room-panel-state="open"]');
        if (!el) return false;
        const r = el.getBoundingClientRect();
        return r.x + r.width <= w + 1;
      },
      width,
      { timeout: 10_000 },
    )
    .catch(() => {});
  const panel = await panelEl.boundingBox();
  if (!onScreen(panel, width, height)) {
    die(
      `the room panel is ${panel ? `${Math.round(panel.width)}px at x=${Math.round(panel.x)}` : "missing"} ` +
        `in a ${width}px window - it is off the side, which is the overlap this check is about`,
    );
  }
  await phone.close();

  // -------------------------------------------------------------- the desk
  // The other direction. A phone fix that changes the desktop is a regression
  // wearing a feature's clothes.
  const desk = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  await desk.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await desk.goto(`${base}/chat/general`, { timeout: 20_000 });
  await desk.locator("textarea").first().waitFor({ state: "visible", timeout: 20_000 });

  if (await desk.locator("[data-nav-open]").first().isVisible()) {
    die("the drawer button is showing at 1600px - the nav should be a column there");
  }
  if (await desk.locator("[data-room-panel-toggle]").first().isVisible()) {
    die("the room panel toggle is showing at 1600px - the panel should be a column there");
  }
  const deskNav = await desk.locator("[data-nav]").first().boundingBox();
  if (!deskNav || deskNav.x < 0 || deskNav.width < 200) {
    die(
      `the nav column is ${deskNav ? `${Math.round(deskNav.width)}px at x=${Math.round(deskNav.x)}` : "missing"} on a desk`,
    );
  }
  const deskPanel = await desk.locator("[data-room-panel-state]").first().boundingBox();
  if (!onScreen(deskPanel, 1600, 1000) || !deskPanel || deskPanel.width < 300) {
    die("the room panel is not a visible column at 1600px");
  }
  // AND THE ROOMS ARE STILL IN THEIR OWN SCROLLER, not stacked into one column
  // the way the drawer needs them. The order/overflow switches are the part of
  // this change most likely to leak upwards past the breakpoint.
  const scrolls = await desk.evaluate(() => {
    const el = document.querySelector("[data-room-list]");
    if (!el) return null;
    return { inner: el.scrollHeight, outer: el.clientHeight, y: getComputedStyle(el).overflowY };
  });
  if (!scrolls || scrolls.y !== "auto") {
    die(
      `the rooms list is overflow-y:${scrolls ? scrolls.y : "missing"} on a desk - it must scroll in itself`,
    );
  }
  await desk.close();

  console.log(
    `phone 390x664: main ${Math.round(main.width)}px, composer ${Math.round(composer.width)}px, ` +
      `${reachable}/${total} rooms reachable, panel fits. desk 1600: nav column, panel column, rooms scroll.`,
  );
} finally {
  await browser.close();
}
