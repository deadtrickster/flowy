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

// ONE REPORT FOR EVERY ARM, not the first one to fail.
//
// The arms fail DIFFERENTLY - 360 cannot close the panel, 880 closes it and
// cannot reopen - and the second is the defect the operator actually reported.
// A check that exits on the first arm hides the reported bug behind a different
// one found at a width nobody complained about, and whoever fixes the first
// would then see green and believe both were done.
const failures = [];
const fail = (why) => {
  failures.push(why);
};

const browser = await chromium.launch();
try {
  // BOTH OF THE OPERATOR'S WIDTHS, because they fail differently and only one
  // of them is the report. A Fold 8 is 360 folded and 880 unfolded, and both are
  // under lg, so the panel is a drawer at each.
  //
  //   360   the panel is w-[26rem] max-w-full at z-40, so it covers the WHOLE
  //         viewport - backdrop underneath at z-30, toggle in the header behind
  //         it. Nothing can close it.
  //   880   the panel is 416px of 880, so the backdrop IS reachable and it
  //         closes. Then the same thread will not open it again, which is what
  //         the operator reported: "threads are one time thing".
  //
  // A single 390px arm finds only the first and would have left the reported
  // defect unmeasured while looking like it had covered it.
  for (const [armName, width, height] of [
    ["a folded phone", 360, 780],
    ["an unfolded fold", 880, 1100],
  ]) {
    const page = await browser.newPage({
      viewport: { width, height },
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
      fail(`no thread control on screen at ${width}px (${armName}), so there is nothing to open - this run
measured nothing rather than finding the pane healthy`);
      await page.close();
      continue;
    }

    // FIRST OPEN, the control. If this fails the defect is the one
    // thread-on-a-phone-check already covers, and saying so keeps the two apart.
    await chip.click({ timeout: 10_000 });
    await page.waitForTimeout(900);
    if (!(await onScreen())) {
      fail(`the FIRST open did not bring the panel on screen at ${width}px (${armName}). That is the defect
thread-on-a-phone-check covers, not this one - fix that first, because this check cannot
say anything about reopening a pane that never opened.`);
      await page.close();
      continue;
    }

    // CLOSE IT THE WAY A PERSON CAN, and find out which way that is.
    //
    // Both close controls can be UNREACHABLE at this width, which is the first
    // thing this check found. The panel is w-[26rem] max-w-full at z-40, so on a
    // 390px screen it covers the whole viewport - and the backdrop is fixed
    // inset-0 at z-30, underneath it everywhere. There is no "outside" left to
    // tap. The toggle lives in the room header, under the panel for the same
    // reason. Playwright reports this as "aside ... subtree intercepts pointer
    // events" rather than as a missing element, which is why it reads like a
    // flaky click and is not one.
    //
    // So each control is TRIED with a short timeout and the outcome recorded. A
    // panel that cannot be closed is a worse defect than one that cannot reopen,
    // and it must be reported as itself rather than as a timeout.
    const tryClose = async (selector) => {
      const el = page.locator(selector).first();
      if ((await el.count()) === 0) return "absent";
      try {
        await el.click({ timeout: 4_000 });
        return "clicked";
      } catch {
        return "covered";
      }
    };
    const viaClose = await tryClose("[data-room-panel-close]");
    const viaBackdrop =
      viaClose === "clicked" ? "not tried" : await tryClose("[data-room-panel-backdrop]");
    const viaToggle =
      viaClose === "clicked" || viaBackdrop === "clicked"
        ? "not tried"
        : await tryClose("[data-room-panel-toggle]");
    await page.waitForTimeout(600);

    if (await onScreen()) {
      fail(`at ${width}px (${armName}) the thread panel will not close: close ${viaClose}, backdrop ${viaBackdrop}, toggle ${viaToggle}.
The panel is w-[26rem] max-w-full at z-40, so on a screen this narrow it covers the whole
viewport - the backdrop sits at z-30 underneath it and the toggle is in the header behind
it. A reader who opens a thread on a phone has no control left to press.
That is a bigger defect than the reopen this check was written for, and it is reported
here rather than as a click timeout because the browser calls it "subtree intercepts
pointer events", which reads like flake.`);
      await page.close();
      continue;
    }

    // AND OPEN IT AGAIN, with the same control. This is the assertion.
    await chip.click({ timeout: 10_000 });
    await page.waitForTimeout(900);
    if (!(await onScreen())) {
      const said = await panel.getAttribute("data-room-panel-state");
      const box = await panel.boundingBox();
      fail(`the thread pane opened, closed, and would not open again at ${width}px (${armName}).
It says data-room-panel-state=${JSON.stringify(said)} and its box sits at x=${
        box ? Math.round(box.x) : "none"
      } of a ${width}px window.
Pressing the same thread twice is the same URL, and the drawer is opened by a URL
change - so the second press asks for something already asked for and nothing opens.
The operator: "threads are one time thing".`);
      // AND STOP, so this arm cannot also print its success line. Converting
      // die() to fail() removed the exit that used to end the arm, and the
      // three branches above got a continue while this one did not - so a run
      // that found the reported defect ALSO announced "opened, closed and
      // opened again" for the same width. A check that reports both outcomes
      // for one arm is worse than one that reports neither.
      await page.close();
      continue;
    }

    console.log(`${armName} (${width}px): the thread pane opened, closed and opened again`);
    await page.close();
  }
} finally {
  await browser.close();
}

if (failures.length > 0) {
  die(failures.join("\n\n"));
}
