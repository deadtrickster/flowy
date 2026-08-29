/**
 * OPENING A THREAD SHOWS THE THREAD, ON A PHONE TOO.
 *
 *   node scripts/thread-on-a-phone-check.mjs BASE_URL TOKEN ROOM
 *
 * Below lg the room panel is not a column, it is a drawer: fixed to the right
 * edge and translated off-screen unless it has been opened. Nothing opened it
 * when a thread was opened, so pressing "N replies" on a phone changed the URL,
 * selected the thread pane, and drew the room with NO VISIBLE CHANGE. Reported
 * from a Fold 8, whose folded and unfolded widths are both under lg - which is
 * why neither of them worked.
 *
 * MEASURED AT A NARROW VIEWPORT, because that is the only place it fails. The
 * desktop arm is the control: the same click at 1600px has always worked, and a
 * check that only ran there would have been green throughout.
 *
 * ASSERTED AS GEOMETRY, not as a state attribute. `data-room-panel-state=open`
 * is set by the same code that would be fixed here, so a check reading it would
 * pass on a drawer still sitting off the right edge. What is asserted is where
 * the panel actually IS: its box has to overlap the viewport.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/thread-on-a-phone-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  for (const [name, width, height] of [
    ["a phone", 390, 844],
    ["a folded phone", 360, 780],
    ["an unfolded phone", 880, 1100],
  ]) {
    const page = await browser.newPage({
      viewport: { width, height },
      isMobile: true,
      hasTouch: true,
    });
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
    // WAIT FOR THE ROOM, not for a guess at how long it takes. The fold that
    // puts "N replies" on a thread's head row is computed from the messages, so
    // asking before they are drawn finds nothing and says the control is
    // missing when it is merely early.
    await page
      .locator("[data-message]")
      .first()
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => {});
    await page.waitForTimeout(800);

    // THE `thread <id>` CONTROL, not the "N replies" chip. The chip only draws
    // on a thread's head row when its replies are FOLDED, which needs a room
    // busy enough to fold them; the thread link is on every message that is in
    // a thread, and is what a person presses to go and look at one.
    const chip = page.locator("text=/^thread [0-9A-Z]+$/").first();
    if ((await chip.count()) === 0) {
      const drew = await page
        .locator("[data-message]")
        .count()
        .catch(() => 0);
      const said = await page.locator("[data-message]").allInnerTexts();
      die(`no thread control on screen at ${width}px with ${drew} message(s) drawn, so there is
nothing to open. The rows say: ${JSON.stringify(said).slice(0, 400)}`);
    }
    await chip.click({ timeout: 10_000 });
    await page.waitForTimeout(1200);

    // WHERE THE PANEL IS, not what it says about itself.
    const panel = page.locator("[data-room-panel-state]").first();
    const box = await panel.boundingBox();
    if (!box) {
      die(
        `at ${width}px (${name}) the room panel is not in the layout at all after opening a thread`,
      );
    }
    const onScreen = box.x < width - 8 && box.x + box.width > 8;
    if (!onScreen) {
      const said = await panel.getAttribute("data-room-panel-state");
      die(`at ${width}px (${name}) the thread was opened and the panel sits at x=${Math.round(
        box.x,
      )} of a ${width}px window - off the edge, so nothing is shown.
It says data-room-panel-state=${JSON.stringify(said)}, which is why this arm
measures the box instead of believing it.`);
    }

    // AND IT IS THE THREAD, not whichever pane was last open.
    const body = page.locator('[data-room-pane-body="thread"]');
    if ((await body.count()) === 0) {
      die(
        `at ${width}px the panel is on screen after opening a thread and is not showing the thread`,
      );
    }
    await page.close();
  }

  // THE CONTROL. At a desktop width the panel is a column and always shown -
  // this arm is what says the fix did not simply force a drawer open everywhere.
  const wide = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  await wide.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await wide.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await wide.waitForTimeout(1200);
  const panel = wide.locator("[data-room-panel-state]").first();
  const box = await panel.boundingBox();
  if (!box || box.x + box.width < 8) {
    die("at 1600px the room panel is not on screen, and there it is a column that always is");
  }

  console.log(
    "opening a thread shows it at 390, 360 and 880 wide, and the panel is still a column at 1600",
  );
} finally {
  await browser.close();
}
