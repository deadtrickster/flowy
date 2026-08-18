/**
 * A person can select and copy what was said in the room.
 *
 * Asserted on a REAL SELECTION in a real browser, because the bug this covers
 * was invisible everywhere else: every message row was a <button>, and a
 * browser refuses to select a button's text - it treats a drag inside one as a
 * click on a control. The markup was correct, the CSS was correct, the poll
 * repaint was innocent, and the transcript could not be copied. Reported by
 * the user, who was the only one positioned to notice.
 *
 *   node scripts/copy-check.mjs BASE_URL TOKEN
 *
 * It drags across a message the way a person does and reads back what the
 * selection actually contains. A check that asserted the row is a <div> would
 * pass on any future element that happens not to be a button and still miss
 * the next thing that makes text unselectable.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/copy-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  // The witness that this run measured anything at all. A room that never
  // painted a message would report "nothing selected", which is exactly what
  // the bug looks like - so an empty transcript has to fail differently.
  await page.waitForSelector("main [data-body]", { timeout: 15_000 }).catch(() => {});
  const bodies = page.locator("main [data-body]");
  const count = await bodies.count();
  if (count === 0) {
    console.error("no message bodies were rendered, so nothing about copying was tested");
    process.exit(1);
  }

  const line = bodies.nth(count - 1);
  // SCROLL TO IT FIRST. The transcript is thousands of pixels tall, so the
  // last message sits far outside the viewport and a drag at its coordinates
  // lands on nothing - which fails identically to text that cannot be
  // selected. The first version of this check did exactly that and nearly
  // convicted a working fix.
  await line.scrollIntoViewIfNeeded();
  await page.waitForTimeout(300);
  const box = await line.boundingBox();
  if (!box) {
    console.error("the last message has no box on screen, so there was nothing to drag across");
    process.exit(1);
  }

  // The gesture, not an API call: selectText() and setting a Range both
  // succeed on a <button> too, because neither goes through the browser's own
  // decision about what a drag inside that element means. Only a real pointer
  // gesture asks that question.
  //
  // NEAR THE TOP OF THE BOX, not its centre. A long message is hundreds of
  // pixels tall and the text sits in the first line or two; a drag through the
  // vertical middle crosses empty space and selects nothing, which fails
  // exactly like unselectable text. That mistake convicted a working fix
  // twice before the coordinates were printed.
  const y = box.y + 8;
  await page.mouse.move(box.x + 8, y);
  await page.mouse.down();
  for (let i = 1; i <= 20; i++) {
    await page.mouse.move(box.x + 8 + i * 8, y);
  }
  await page.mouse.up();

  const selected = (await page.evaluate(() => window.getSelection()?.toString() ?? "")).trim();
  if (selected.length === 0) {
    console.error(
      "dragging across a message selected nothing - the transcript is not copyable.\n" +
        "  The usual cause is the row being a <button>: browsers do not select button text.",
    );
    process.exit(1);
  }

  console.log(`ok  a message can be selected: ${JSON.stringify(selected.slice(0, 60))}`);
} finally {
  await browser.close();
}
