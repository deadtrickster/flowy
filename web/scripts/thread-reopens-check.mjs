/**
 * A THREAD PANE OPENS, CLOSES, AND OPENS AGAIN.
 *
 *   node scripts/thread-reopens-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, from a Fold 8: "on the phone threads are one time thing - i
 * replied to the host once, cloded thread pane and it foesnt come back anymre."
 *
 * WHY thread-on-a-phone-check DID NOT SEE THIS. That check presses a thread
 * control and asserts the panel's box overlaps the viewport - and it passes,
 * because the FIRST open works. It never closes the panel, so a defect that
 * only appears on the second open is outside what it looks at. A check that
 * opens once cannot distinguish "opens" from "opens once".
 *
 * WHAT THE SECOND OPEN DEPENDS ON. Below lg the panel is a drawer, and
 * ChatRoom opens it from a URL change:
 *
 *     useEffect(() => { if (linked || asked) setPanelOpen(true) }, [linked, asked])
 *
 * Both come from useParams. Pressing the same thread again is the same URL, so
 * the effect does not re-run and nothing tells the drawer to open. The state
 * that closed it is not the state that opens it.
 *
 * SO THE ASSERTION IS THE SECOND OPEN, and the first is kept beside it as the
 * control - a run where the first open failed would be a different defect and
 * must not read as this one.
 *
 * GEOMETRY, NOT THE STATE ATTRIBUTE. data-room-panel-state is written by the
 * same code a fix would touch, so a check reading it can pass while the drawer
 * sits off the right edge. What is asserted is where the box IS. That lesson
 * cost a red on the rail an hour before this was written, where 22 labels
 * shared a left edge perfectly at x=-244.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/thread-reopens-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const width = 390;
  const page = await browser.newPage({
    viewport: { width, height: 844 },
    isMobile: true,
    hasTouch: true,
  });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await page
    .locator("[data-message]")
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  await page.waitForTimeout(600);

  const panel = page.locator("[data-room-panel-state]").first();
  const onScreen = async () => {
    const box = await panel.boundingBox();
    if (!box) return false;
    return box.x < width - 8 && box.x + box.width > 8;
  };

  const chip = page.locator("text=/^thread [0-9A-Z]+$/").first();
  if ((await chip.count()) === 0) {
    die(`no thread control on screen at ${width}px, so there is nothing to open - this run
measured nothing rather than finding the pane healthy`);
  }

  // FIRST OPEN, the control. If this fails the defect is the one
  // thread-on-a-phone-check already covers, and saying so keeps the two apart.
  await chip.click({ timeout: 10_000 });
  await page.waitForTimeout(900);
  if (!(await onScreen())) {
    die(`the FIRST open did not bring the panel on screen at ${width}px. That is the defect
thread-on-a-phone-check covers, not this one - fix that first, because this check cannot
say anything about reopening a pane that never opened.`);
  }

  // CLOSE IT the way a person does.
  const backdrop = page.locator("[data-room-panel-backdrop]").first();
  if ((await backdrop.count()) > 0) {
    await backdrop.click({ timeout: 10_000 });
  } else {
    await page.locator("[data-room-panel-toggle]").first().click({ timeout: 10_000 });
  }
  await page.waitForTimeout(600);
  if (await onScreen()) {
    die(`the panel is still on screen after pressing its own close control at ${width}px, so
this run cannot tell whether reopening works - it never shut`);
  }

  // AND OPEN IT AGAIN, with the same control. This is the assertion.
  await chip.click({ timeout: 10_000 });
  await page.waitForTimeout(900);
  if (!(await onScreen())) {
    const said = await panel.getAttribute("data-room-panel-state");
    const box = await panel.boundingBox();
    die(`the thread pane opened, closed, and would not open again at ${width}px.
It says data-room-panel-state=${JSON.stringify(said)} and its box sits at x=${
      box ? Math.round(box.x) : "none"
    } of a ${width}px window.
Pressing the same thread twice is the same URL, and the drawer is opened by a URL
change - so the second press asks for something already asked for and nothing opens.
The operator: "threads are one time thing".`);
  }

  console.log(`the thread pane at ${width}px opened, closed on its backdrop, and opened again`);
} finally {
  await browser.close();
}
