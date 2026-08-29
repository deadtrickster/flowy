/**
 * A LINE OF PROSE IS A LINE SOMEBODY CAN READ.
 *
 *   node scripts/measure-check.mjs BASE_URL TOKEN ROOM
 *
 * Measured on the live room before this existed: 14px text, 20px leading, an
 * 874px column - 118 characters per line. The comfortable measure is 45 to 75
 * and the usual target is 66. At 118 the eye loses the line it is returning to,
 * which is what made a room of ordinary paragraphs read as a wall.
 *
 * The markdown rhythm was never the problem and this does not touch it.
 *
 * ASSERTED AS A NUMBER, NEVER AS A CLASS. `max-width: 72ch` in a stylesheet is
 * a fact about a file; a class check passes on a rule overridden three files
 * later, on a container that never receives it, or on a body that is empty.
 * This measures the drawn text: how many characters fit on one line of the
 * element a person is actually reading, and the leading it is set at.
 *
 * AND AT TWO WIDTHS. A cap that only holds at one viewport is a cap that did
 * not apply - a narrow window is already under the bound, so the wide one is
 * the arm with teeth.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/measure-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const read = async () =>
    await page.evaluate(() => {
      // The paragraph a reader is reading, not the card around it: the card is
      // meant to stay wide and only the text is capped.
      const para = document.querySelector(".report-body > p");
      if (!para) return null;
      const cs = getComputedStyle(para);
      const width = para.getBoundingClientRect().width;
      // Characters per line from the ACTUAL glyph advance of this font at this
      // size, rather than from a guess about what a character is worth.
      const probe = document.createElement("span");
      probe.textContent = "abcdefghijklmnopqrstuvwxyz";
      probe.style.cssText = `font:${cs.font};position:absolute;visibility:hidden;white-space:pre`;
      document.body.appendChild(probe);
      const per = probe.getBoundingClientRect().width / 26;
      probe.remove();
      return {
        width: Math.round(width),
        chars: Math.round(width / per),
        size: Number.parseFloat(cs.fontSize),
        leading: Number.parseFloat(cs.lineHeight),
      };
    });

  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await page.locator(".report-body").first().waitFor({ state: "visible", timeout: 20_000 });
  await page.waitForTimeout(500);

  const wide = await read();
  if (!wide) die("no message paragraph on screen, so there is no line to measure");

  // THE CAP. 75 is the top of the comfortable band; a little over is the ch
  // unit disagreeing with the real average glyph, which is why this is not 72.
  if (wide.chars > 80) {
    die(`a line of prose is ${wide.chars} characters at a 1600px window (${wide.width}px of text).

45 to 75 is the comfortable measure. Past it the eye loses the line it is
returning to, which is the defect this bound exists to prevent.`);
  }
  // AND NOT ABSURDLY NARROW. A rule that capped at 20ch would satisfy the arm
  // above and be worse than what it replaced.
  if (wide.chars < 40) {
    die(`a line of prose is only ${wide.chars} characters - too narrow to read comfortably`);
  }

  const ratio = wide.leading / wide.size;
  if (ratio < 1.5) {
    die(
      `the leading is ${wide.leading}px on ${wide.size}px text, a ratio of ${ratio.toFixed(
        2,
      )}. Body prose wants 1.5 or more; tighter is what makes a long line unreadable.`,
    );
  }

  // NARROW WINDOW: the text must follow the column down rather than overflow it.
  await page.setViewportSize({ width: 900, height: 1000 });
  await page.waitForTimeout(400);
  const narrow = await read();
  if (!narrow) die("the message paragraph disappeared at 900px");
  if (narrow.width > wide.width) {
    die(`the text is ${narrow.width}px in a 900px window and ${wide.width}px in a 1600px one -
it grew as the window shrank, so it is not following its column`);
  }

  console.log(
    `prose measures ${wide.chars} characters at 1600px and ${narrow.chars} at 900px, leading ${ratio.toFixed(2)} of the font size`,
  );
} finally {
  await browser.close();
}
